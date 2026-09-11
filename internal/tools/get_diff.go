package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/OtavioMendes12/Sentinel-AI/internal/domain"
	"github.com/OtavioMendes12/Sentinel-AI/internal/github"
)

// NewGetDiff returns the get_diff tool over the files changed by the pull
// request. The files are fetched once per review, before the loop starts.
func NewGetDiff(files []domain.ChangedFile) Tool {
	return Tool{
		Name: "get_diff",
		Description: "Without a path, list the files changed by the pull request. " +
			"With a path, return that file's full diff (useful when the diff in the prompt was truncated).",
		InputSchema: &Schema{
			Type: TypeObject,
			Properties: map[string]*Schema{
				"path": {Type: TypeString, Description: "A changed file, relative to the repository root.", MaxLength: 512},
			},
		},
		Risk: RiskRead,
		Handler: func(_ context.Context, args json.RawMessage) (string, error) {
			return getDiff(files, args)
		},
	}
}

func getDiff(files []domain.ChangedFile, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("decoding arguments: %w", err)
	}

	if strings.TrimSpace(in.Path) == "" {
		var b strings.Builder
		fmt.Fprintf(&b, "%d changed files\n", len(files))
		for _, f := range files {
			fmt.Fprintf(&b, "%-8s %s (+%d -%d)", f.Status, f.Path, f.Additions, f.Deletions)
			if f.PreviousPath != "" {
				fmt.Fprintf(&b, " renamed from %s", f.PreviousPath)
			}
			if f.Patch == "" {
				b.WriteString(" [no textual diff]")
			}
			b.WriteByte('\n')
		}
		return b.String(), nil
	}

	want := path.Clean(strings.TrimSpace(in.Path))
	for _, f := range files {
		if f.Path == want || f.PreviousPath == want {
			return github.RenderDiff([]domain.ChangedFile{f}), nil
		}
	}
	return "", fmt.Errorf("%q is not changed by this pull request; call get_diff without a path to list changed files", in.Path)
}
