package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OtavioMendes12/Sentinel-AI/internal/sandbox"
)

const (
	defaultSearchResults = 50
	maxSearchResults     = 100
)

// searchExcludes mirrors the blocked paths of read_file, so search results
// cannot be used to read blocked files either.
var searchExcludes = []string{
	"!.git", "!.env", "!.env.*", "!.git-credentials", "!.netrc", "!.npmrc", "!.pypirc", "!.dockercfg",
	"!credentials.json", "!id_rsa*", "!id_dsa*", "!id_ecdsa*", "!id_ed25519*",
	"!*.pem", "!*.key", "!*.p12", "!*.pfx", "!*.jks", "!*.keystore",
}

// NewSearchCode returns the search_code tool, backed by ripgrep.
//
// ripgrep's regex engine runs in linear time, so a pattern chosen by the
// model cannot cause catastrophic backtracking. The query is always passed
// after --regexp and paths after --, so neither can be read as a flag (for
// example --pre, which would execute a program).
func NewSearchCode(ws *Workspace, runner *sandbox.Runner) Tool {
	return Tool{
		Name: "search_code",
		Description: "Search the repository with ripgrep to find definitions, callers, implementations and tests. " +
			"Returns matching lines as path:line:text. Respects .gitignore and skips hidden files.",
		InputSchema: &Schema{
			Type: TypeObject,
			Properties: map[string]*Schema{
				"query":       {Type: TypeString, Description: "Text to search for (a regular expression when regex is true).", MaxLength: 256},
				"path":        {Type: TypeString, Description: "Restrict the search to this file or directory.", MaxLength: 512},
				"regex":       {Type: TypeBoolean, Description: "Interpret query as a regular expression (Rust syntax). Default false."},
				"max_results": {Type: TypeInteger, Description: fmt.Sprintf("Maximum matching lines to return (default %d).", defaultSearchResults), Minimum: Bound(1), Maximum: Bound(maxSearchResults)},
			},
			Required: []string{"query"},
		},
		Risk: RiskRead,
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return searchCode(ctx, ws, runner, args)
		},
	}
}

func searchCode(ctx context.Context, ws *Workspace, runner *sandbox.Runner, args json.RawMessage) (string, error) {
	var in struct {
		Query      string `json:"query"`
		Path       string `json:"path"`
		Regex      bool   `json:"regex"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("decoding arguments: %w", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return "", errors.New("query must not be empty")
	}
	target, err := cleanPath(in.Path, true)
	if err != nil {
		return "", err
	}
	if err := ws.rejectSymlinks(target); err != nil {
		return "", err
	}
	limit := in.MaxResults
	if limit == 0 {
		limit = defaultSearchResults
	}

	rgArgs := []string{
		"--no-config", "--line-number", "--no-heading", "--with-filename", "--color=never",
		"--max-columns=300", "--max-columns-preview", "--max-filesize=1M", "--max-count=20",
	}
	for _, g := range searchExcludes {
		rgArgs = append(rgArgs, "--glob", g)
	}
	if !in.Regex {
		rgArgs = append(rgArgs, "--fixed-strings")
	}
	// Always pass a path: without one, ripgrep may search stdin instead.
	searchPath := "./"
	if target != "." {
		searchPath += target
	}
	rgArgs = append(rgArgs, "--regexp", in.Query, "--", searchPath)

	res, err := runner.Run(ctx, "rg", rgArgs...)
	if err != nil {
		return "", err
	}
	switch res.ExitCode {
	case 0:
	case 1:
		return fmt.Sprintf("no matches for %q", in.Query), nil
	default:
		msg, _, _ := strings.Cut(strings.TrimSpace(res.Stderr), "\n")
		return "", fmt.Errorf("search failed: %s", msg)
	}

	var lines []string
	for _, l := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
		lines = append(lines, strings.TrimPrefix(l, "./"))
	}
	shown := lines[:min(len(lines), limit)]

	var b strings.Builder
	if len(lines) > limit || res.Truncated {
		fmt.Fprintf(&b, "showing the first %d matching lines; narrow the query or path for more\n", len(shown))
	} else {
		fmt.Fprintf(&b, "%d matching lines\n", len(shown))
	}
	b.WriteString(strings.Join(shown, "\n"))
	return b.String(), nil
}
