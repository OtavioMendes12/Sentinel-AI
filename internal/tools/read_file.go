package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	maxReadFileBytes = 1 << 20
	maxReadLines     = 400
)

// NewReadFile returns the read_file tool: numbered lines of a repository
// file, so the model can cite exact line numbers in findings.
func NewReadFile(ws *Workspace) Tool {
	return Tool{
		Name: "read_file",
		Description: fmt.Sprintf("Read a file of the repository at the pull request's head commit. "+
			"Returns numbered lines, at most %d per call; use start_line and end_line for large files.", maxReadLines),
		InputSchema: &Schema{
			Type: TypeObject,
			Properties: map[string]*Schema{
				"path":       {Type: TypeString, Description: "Path relative to the repository root.", MaxLength: 512},
				"start_line": {Type: TypeInteger, Description: "First line to return (default 1).", Minimum: Bound(1)},
				"end_line":   {Type: TypeInteger, Description: "Last line to return.", Minimum: Bound(1)},
			},
			Required: []string{"path"},
		},
		Risk:    RiskRead,
		Handler: ws.readFile,
	}
}

func (w *Workspace) readFile(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("decoding arguments: %w", err)
	}
	p, err := cleanPath(in.Path, false)
	if err != nil {
		return "", err
	}
	if err := w.rejectSymlinks(p); err != nil {
		return "", err
	}

	data, err := w.readRegularFile(p)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return "", fmt.Errorf("%q is a binary file", p)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // trailing newline
	}
	total := len(lines)
	if total == 0 {
		return fmt.Sprintf("%s (empty file)", p), nil
	}

	start := max(in.StartLine, 1)
	if start > total {
		return "", fmt.Errorf("start_line %d is past the end of %q (%d lines)", start, p, total)
	}
	end := in.EndLine
	if end == 0 || end > total {
		end = total
	}
	if end < start {
		return "", fmt.Errorf("end_line %d is before start_line %d", end, start)
	}
	limited := end-start+1 > maxReadLines
	if limited {
		end = start + maxReadLines - 1
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s (lines %d-%d of %d)\n", p, start, end, total)
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%6d  %s\n", i, lines[i-1])
	}
	if limited {
		fmt.Fprintf(&b, "[more lines available: call read_file with start_line=%d]\n", end+1)
	}
	return b.String(), nil
}

func (w *Workspace) readRegularFile(p string) ([]byte, error) {
	f, err := w.root.Open(p)
	if err != nil {
		return nil, fmt.Errorf("opening %q: %w", p, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	switch {
	case err != nil:
		return nil, fmt.Errorf("inspecting %q: %w", p, err)
	case info.IsDir():
		return nil, fmt.Errorf("%q is a directory; use search_code or get_diff to find files", p)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%q is not a regular file", p)
	case info.Size() > maxReadFileBytes:
		return nil, fmt.Errorf("%q is too large to read (%d bytes)", p, info.Size())
	}
	return io.ReadAll(io.LimitReader(f, maxReadFileBytes))
}
