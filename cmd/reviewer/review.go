package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/OtavioMendes12/Sentinel-AI/internal/agent"
	"github.com/OtavioMendes12/Sentinel-AI/internal/config"
	"github.com/OtavioMendes12/Sentinel-AI/internal/domain"
	"github.com/OtavioMendes12/Sentinel-AI/internal/github"
	"github.com/OtavioMendes12/Sentinel-AI/internal/llm/openai"
	"github.com/OtavioMendes12/Sentinel-AI/internal/redact"
	"github.com/OtavioMendes12/Sentinel-AI/internal/sandbox"
	"github.com/OtavioMendes12/Sentinel-AI/internal/tools"
)

const (
	githubRequestTimeout = 30 * time.Second
	commandOutputLimit   = 256 << 10
)

// review runs one pull request review end to end.
func review(ctx context.Context, cfg *config.Config, logger *slog.Logger, redactor *redact.Redactor, out io.Writer) error {
	if !cfg.DryRun {
		// Checked before spending any tokens.
		return errors.New("publishing is not implemented yet (stage 4); set DRY_RUN=true")
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.Agent.Timeout)
	defer cancel()

	logger = logger.With(
		"execution_id", newExecutionID(),
		"repository", cfg.GitHub.Repository,
		"pull_request", cfg.GitHub.PRNumber,
	)

	gh := github.NewClient(cfg.GitHub.APIURL, cfg.GitHub.Token.Reveal(), &http.Client{Timeout: githubRequestTimeout})
	pr, err := gh.PullRequest(ctx, cfg.GitHub.Repository, cfg.GitHub.PRNumber)
	if err != nil {
		return err
	}
	logger = logger.With("head_sha", pr.HeadSHA)
	if pr.State != "open" {
		logger.Info("pull request is not open; nothing to review", "state", pr.State)
		return nil
	}
	files, err := gh.PullRequestFiles(ctx, cfg.GitHub.Repository, cfg.GitHub.PRNumber)
	if err != nil {
		return err
	}

	ws, err := tools.OpenWorkspace(cfg.RepoPath)
	if err != nil {
		return err
	}
	defer func() { _ = ws.Close() }()
	runner, err := sandbox.New(cfg.RepoPath, commandOutputLimit, "rg")
	if err != nil {
		return err
	}
	registry := tools.NewRegistry()
	for _, t := range []tools.Tool{tools.NewGetDiff(files), tools.NewReadFile(ws), tools.NewSearchCode(ws, runner)} {
		if err := registry.Register(t); err != nil {
			return err
		}
	}

	orch, err := agent.New(agent.Options{
		LLM:      openai.New(cfg.OpenAI.BaseURL, cfg.OpenAI.APIKey.Reveal(), cfg.OpenAI.Model, &http.Client{Timeout: cfg.OpenAI.Timeout}),
		Registry: registry,
		Policy:   agent.Policy{MaxSteps: cfg.Agent.MaxSteps},
		Redactor: redactor,
		Logger:   logger,
		Language: cfg.Agent.Language,
	})
	if err != nil {
		return err
	}

	logger.Info("review started", "changed_files", len(files), "model", cfg.OpenAI.Model)
	start := time.Now()
	res, err := orch.Run(ctx, agent.Task{
		Repository:  pr.Repository,
		PRNumber:    pr.Number,
		HeadSHA:     pr.HeadSHA,
		Title:       pr.Title,
		Description: pr.Body,
		Diff:        github.RenderDiff(files),
	})

	var publish, suppress []domain.Finding
	if res != nil && res.Review != nil {
		publish, suppress = res.Review.Partition(cfg.Agent.MinConfidence)
	}
	logSummary(logger, res, time.Since(start), len(publish), len(suppress), err)
	if err != nil {
		return fmt.Errorf("review failed: %w", err)
	}

	return printReview(out, res.Review.Summary, publish, suppress)
}

func logSummary(logger *slog.Logger, res *agent.Result, elapsed time.Duration, published, suppressed int, err error) {
	attrs := []any{"duration_ms", elapsed.Milliseconds(), "findings_published", published, "findings_suppressed", suppressed}
	if res != nil {
		attrs = append(attrs,
			"steps", res.Steps,
			"tool_calls", res.ToolCalls,
			"rejected_calls", res.RejectedCalls,
			"tool_usage", res.ToolUsage,
			"input_tokens", res.Usage.InputTokens,
			"output_tokens", res.Usage.OutputTokens,
			"dropped_findings", res.DroppedFindings,
		)
	}
	if err != nil {
		logger.Error("review finished with error", append(attrs, "error", err)...)
		return
	}
	logger.Info("review finished", attrs...)
}

// printReview writes the dry-run result. Suppressed findings are included so
// the confidence threshold can be tuned.
func printReview(out io.Writer, summary string, publish, suppress []domain.Finding) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(struct {
		Summary    string           `json:"summary"`
		Findings   []domain.Finding `json:"findings"`
		Suppressed []domain.Finding `json:"suppressed_findings"`
	}{summary, nonNil(publish), nonNil(suppress)})
}

func nonNil(f []domain.Finding) []domain.Finding {
	if f == nil {
		return []domain.Finding{}
	}
	return f
}

// newExecutionID identifies one run across log lines.
func newExecutionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b)
}
