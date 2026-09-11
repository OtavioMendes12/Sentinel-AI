// Package agent implements the review agent: a bounded loop in which the
// model investigates a pull request through registered tools and ends by
// submitting a structured review.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/OtavioMendes12/Sentinel-AI/internal/domain"
	"github.com/OtavioMendes12/Sentinel-AI/internal/llm"
	"github.com/OtavioMendes12/Sentinel-AI/internal/redact"
	"github.com/OtavioMendes12/Sentinel-AI/internal/tools"
)

// ErrStepLimit is returned when the step budget runs out before a valid
// review is submitted. Nothing should be published in that case.
var ErrStepLimit = errors.New("step limit reached before a valid review was submitted")

// Policy bounds a review run. Zero values fall back to defaults.
type Policy struct {
	MaxSteps            int           // model round trips; the last one may only submit
	MaxToolCallsPerStep int           // extra calls in one response are rejected
	MaxToolOutputBytes  int           // tool output is truncated beyond this
	MaxDiffBytes        int           // diff in the initial prompt is truncated beyond this
	ToolTimeout         time.Duration // deadline for a single tool call
	MaxRisk             tools.Risk    // highest risk the model may invoke; never RiskWrite
}

const (
	defaultMaxSteps            = 10
	defaultMaxToolCallsPerStep = 5
	defaultMaxToolOutputBytes  = 16 << 10
	defaultMaxDiffBytes        = 64 << 10
	defaultToolTimeout         = 60 * time.Second
)

func (p Policy) withDefaults() Policy {
	if p.MaxSteps <= 0 {
		p.MaxSteps = defaultMaxSteps
	}
	if p.MaxToolCallsPerStep <= 0 {
		p.MaxToolCallsPerStep = defaultMaxToolCallsPerStep
	}
	if p.MaxToolOutputBytes <= 0 {
		p.MaxToolOutputBytes = defaultMaxToolOutputBytes
	}
	if p.MaxDiffBytes <= 0 {
		p.MaxDiffBytes = defaultMaxDiffBytes
	}
	if p.ToolTimeout <= 0 {
		p.ToolTimeout = defaultToolTimeout
	}
	if p.MaxRisk == 0 {
		p.MaxRisk = tools.RiskExecute
	}
	return p
}

// Options are the orchestrator's dependencies.
type Options struct {
	LLM      llm.Client
	Registry *tools.Registry
	Policy   Policy
	Redactor *redact.Redactor // scrubs tool output before it reaches the model
	Logger   *slog.Logger
}

// Orchestrator runs the agent loop. It is safe for concurrent use: all
// per-review state lives in Run.
type Orchestrator struct {
	llm      llm.Client
	policy   Policy
	redactor *redact.Redactor
	logger   *slog.Logger

	exposed    map[string]tools.Tool // tools the model may call
	specs      []llm.ToolSpec        // exposed tools plus submit_review
	submitSpec llm.ToolSpec
}

// New validates the dependencies and computes which tools the model may see.
func New(opts Options) (*Orchestrator, error) {
	if opts.LLM == nil || opts.Registry == nil {
		return nil, errors.New("agent: LLM client and tool registry are required")
	}
	policy := opts.Policy.withDefaults()
	if policy.MaxRisk >= tools.RiskWrite {
		return nil, errors.New("agent: the model must not be allowed to invoke write tools")
	}
	if _, clash := opts.Registry.Lookup(SubmitReviewTool); clash {
		return nil, fmt.Errorf("agent: tool name %q is reserved", SubmitReviewTool)
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	o := &Orchestrator{
		llm:      opts.LLM,
		policy:   policy,
		redactor: opts.Redactor,
		logger:   logger,
		exposed:  make(map[string]tools.Tool),
	}

	for _, t := range opts.Registry.Tools() {
		if t.Risk > policy.MaxRisk {
			continue
		}
		spec, err := toolSpec(t.Name, t.Description, t.InputSchema)
		if err != nil {
			return nil, err
		}
		o.exposed[t.Name] = t
		o.specs = append(o.specs, spec)
	}
	submit, err := toolSpec(SubmitReviewTool, submitReviewDescription, reviewSchema())
	if err != nil {
		return nil, err
	}
	o.submitSpec = submit
	o.specs = append(o.specs, submit)
	return o, nil
}

func toolSpec(name, description string, schema *tools.Schema) (llm.ToolSpec, error) {
	params, err := json.Marshal(schema)
	if err != nil {
		return llm.ToolSpec{}, fmt.Errorf("agent: encoding schema of %s: %w", name, err)
	}
	return llm.ToolSpec{Name: name, Description: description, Parameters: params}, nil
}

// Result describes a finished run. It is returned even when Run fails, so
// the caller can record what happened.
type Result struct {
	Review          *domain.Review // nil unless a valid review was submitted
	Steps           int
	ToolCalls       int            // calls executed by tools
	RejectedCalls   int            // calls refused by guardrails
	ToolUsage       map[string]int // executed calls per tool
	DroppedFindings int            // invalid findings removed on the last step
	Usage           llm.Usage
}

// Run reviews one pull request. The loop ends when the model submits a valid
// review, the step budget is exhausted (ErrStepLimit), ctx is done, or the
// model provider fails.
func (o *Orchestrator) Run(ctx context.Context, task Task) (*Result, error) {
	r := &run{
		o:      o,
		seen:   make(map[string]int),
		result: Result{ToolUsage: make(map[string]int)},
		messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleUser, Content: taskPrompt(task, o.policy.MaxDiffBytes)},
		},
	}

	for step := 1; step <= o.policy.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return &r.result, fmt.Errorf("review aborted before step %d: %w", step, err)
		}
		r.result.Steps = step
		review, err := r.step(ctx, step, step == o.policy.MaxSteps)
		if err != nil {
			return &r.result, err
		}
		if review != nil {
			r.result.Review = review
			return &r.result, nil
		}
	}
	return &r.result, ErrStepLimit
}

// run holds the state of a single review.
type run struct {
	o        *Orchestrator
	messages []llm.Message
	seen     map[string]int // canonical tool call -> step it was first made
	result   Result
}

// step performs one model round trip and handles the tool calls it returns.
// It returns a review once one has been accepted.
func (r *run) step(ctx context.Context, step int, final bool) (*domain.Review, error) {
	specs := r.o.specs
	if final {
		specs = []llm.ToolSpec{r.o.submitSpec}
		r.messages = append(r.messages, llm.Message{Role: llm.RoleUser, Content: finalStepPrompt})
	}

	resp, err := r.o.llm.Complete(ctx, llm.Request{Messages: r.messages, Tools: specs, RequireToolCall: true})
	if err != nil {
		return nil, fmt.Errorf("model request at step %d: %w", step, err)
	}
	r.result.Usage.Add(resp.Usage)

	msg := resp.Message
	msg.Role = llm.RoleAssistant
	r.messages = append(r.messages, msg)

	if len(msg.ToolCalls) == 0 {
		r.messages = append(r.messages, llm.Message{Role: llm.RoleUser, Content: noToolCallPrompt})
		return nil, nil
	}

	executed := 0
	for _, call := range msg.ToolCalls {
		if call.Name == SubmitReviewTool {
			review, err := r.submit(call, final)
			if err == nil {
				return review, nil
			}
			r.reject(step, call, "the review is invalid; fix these problems and call submit_review again:\n"+err.Error())
			continue
		}
		if executed >= r.o.policy.MaxToolCallsPerStep {
			r.reject(step, call, fmt.Sprintf("too many tool calls in one step (limit %d); repeat it in a later step if still needed", r.o.policy.MaxToolCallsPerStep))
			continue
		}
		executed++
		r.respond(call, r.execute(ctx, step, call, final))
	}
	return nil, nil
}

// execute applies the guardrails to a tool call and runs it. The returned
// text is what the model sees as the tool result.
func (r *run) execute(ctx context.Context, step int, call llm.ToolCall, final bool) string {
	tool, ok := r.o.exposed[call.Name]
	switch {
	case !ok:
		return r.rejection(step, call, fmt.Sprintf("tool %q does not exist or is not available", call.Name))
	case final:
		return r.rejection(step, call, "only submit_review can be called in the last step")
	}
	if err := tool.InputSchema.Validate(call.Arguments); err != nil {
		return r.rejection(step, call, "invalid arguments: "+err.Error())
	}
	key := call.Name + " " + canonicalJSON(call.Arguments)
	if first, dup := r.seen[key]; dup {
		return r.rejection(step, call, fmt.Sprintf("identical call already made at step %d; use that result", first))
	}
	r.seen[key] = step

	start := time.Now()
	out, err := r.invoke(ctx, tool, call.Arguments)
	r.result.ToolCalls++
	r.result.ToolUsage[call.Name]++

	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	r.o.logger.Info("tool call",
		"step", step, "tool", call.Name, "risk", tool.Risk.String(), "outcome", outcome,
		"duration_ms", time.Since(start).Milliseconds(), "output_bytes", len(out))

	if err != nil {
		return "error: " + r.o.redactor.Redact(err.Error())
	}
	// Redact before truncating: a cut could split a secret and hide it from the patterns.
	return untrusted(call.Name, truncate(r.o.redactor.Redact(out), r.o.policy.MaxToolOutputBytes))
}

// invoke runs the handler with its own deadline and converts panics into
// errors, since tools parse untrusted content.
func (r *run) invoke(ctx context.Context, tool tools.Tool, args json.RawMessage) (out string, err error) {
	ctx, cancel := context.WithTimeout(ctx, r.o.policy.ToolTimeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			r.o.logger.Error("tool panicked", "tool", tool.Name, "panic", fmt.Sprint(p))
			out, err = "", errors.New("tool failed unexpectedly")
		}
	}()

	out, err = tool.Handler(ctx, args)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("tool timed out after %s", r.o.policy.ToolTimeout)
	}
	return out, err
}

// submit decodes and validates a submitted review. On the last step, invalid
// findings are dropped rather than losing the whole review.
func (r *run) submit(call llm.ToolCall, final bool) (*domain.Review, error) {
	review, err := decodeReview(call.Arguments)
	if err != nil {
		return nil, err
	}
	err = review.Validate()
	if err == nil {
		return &review, nil
	}
	if !final {
		return nil, err
	}

	salvaged, dropped := review.WithoutInvalidFindings()
	if salvaged.Validate() != nil {
		return nil, err
	}
	r.result.DroppedFindings = dropped
	r.o.logger.Warn("dropped invalid findings on the last step", "dropped", dropped)
	return &salvaged, nil
}

func (r *run) respond(call llm.ToolCall, content string) {
	r.messages = append(r.messages, llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: content})
}

// rejection records a refused call and returns the message for the model.
func (r *run) rejection(step int, call llm.ToolCall, reason string) string {
	r.result.RejectedCalls++
	r.o.logger.Warn("tool call rejected", "step", step, "tool", call.Name, "reason", reason)
	return "rejected: " + reason
}

func (r *run) reject(step int, call llm.ToolCall, reason string) {
	r.respond(call, r.rejection(step, call, reason))
}

// canonicalJSON normalizes arguments so that calls differing only in key
// order or whitespace are recognized as duplicates.
func canonicalJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
