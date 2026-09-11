package github

import (
	"fmt"
	"strings"

	"github.com/OtavioMendes12/Sentinel-AI/internal/domain"
)

// RenderDiff builds a unified diff from the per-file patches returned by the
// files endpoint. Using those patches, instead of the whole-PR diff media
// type, avoids GitHub's size limits on the latter and keeps the diff
// consistent with the file list the tools see.
func RenderDiff(files []domain.ChangedFile) string {
	var b strings.Builder
	for _, f := range files {
		oldPath := f.Path
		if f.PreviousPath != "" {
			oldPath = f.PreviousPath
		}
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", oldPath, f.Path)
		switch f.Status {
		case "added":
			b.WriteString("new file\n")
		case "removed":
			b.WriteString("deleted file\n")
		case "renamed":
			fmt.Fprintf(&b, "rename from %s\nrename to %s\n", oldPath, f.Path)
		}
		if f.Patch == "" {
			b.WriteString("(no textual diff: binary or too large; use read_file if needed)\n")
			continue
		}
		fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n%s\n", oldPath, f.Path, strings.TrimRight(f.Patch, "\n"))
	}
	return b.String()
}
