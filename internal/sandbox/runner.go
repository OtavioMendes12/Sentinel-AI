// Package sandbox runs allowlisted external commands on behalf of tools.
//
// It is a process-level sandbox, not an isolation boundary: commands run with
// a scrubbed environment (no secrets), a fixed working directory, a timeout,
// capped output and no shell. Stronger isolation (containers, no network)
// is provided by the CI job that runs the reviewer.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// waitDelay bounds how long Run waits for output pipes after the process is
// killed, in case a grandchild keeps them open.
const waitDelay = 2 * time.Second

// Runner executes allowlisted binaries in a fixed directory.
type Runner struct {
	dir       string
	commands  map[string]string // name -> absolute path, resolved once at startup
	env       []string
	maxOutput int
}

// New resolves every allowed command to an absolute path up front, so a
// binary appearing later on PATH (for example one added by the pull request)
// cannot be picked up mid-review.
func New(dir string, maxOutputBytes int, commands ...string) (*Runner, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolving working directory: %w", err)
	}
	r := &Runner{dir: absDir, commands: make(map[string]string), maxOutput: maxOutputBytes}

	var pathDirs []string
	for _, name := range commands {
		if strings.ContainsRune(name, filepath.Separator) {
			return nil, fmt.Errorf("sandbox: command %q must be a bare name", name)
		}
		path, err := exec.LookPath(name)
		if err != nil {
			return nil, fmt.Errorf("sandbox: command %q not found: %w", name, err)
		}
		if path, err = filepath.Abs(path); err != nil {
			return nil, fmt.Errorf("sandbox: resolving %q: %w", name, err)
		}
		r.commands[name] = path
		if d := filepath.Dir(path); !slices.Contains(pathDirs, d) {
			pathDirs = append(pathDirs, d)
		}
	}
	// A minimal environment: nothing from the parent process, so API keys and
	// tokens can never reach a child process or its output.
	pathDirs = append(pathDirs, "/usr/bin", "/bin")
	r.env = []string{"PATH=" + strings.Join(pathDirs, ":"), "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	return r, nil
}

// Result is the outcome of a command that ran to completion.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool // output exceeded the limit
}

// Run executes an allowlisted command with args passed directly to the
// process (no shell, so no expansion or injection). A non-zero exit status is
// reported in Result, not as an error.
func (r *Runner) Run(ctx context.Context, name string, args ...string) (*Result, error) {
	path, ok := r.commands[name]
	if !ok {
		return nil, fmt.Errorf("sandbox: command %q is not allowed", name)
	}

	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // path comes from the allowlist resolved in New; args go to the process without a shell
	cmd.Dir = r.dir
	cmd.Env = r.env
	cmd.WaitDelay = waitDelay
	configureProcessGroup(cmd)

	stdout := &cappedBuffer{limit: r.maxOutput}
	stderr := &cappedBuffer{limit: r.maxOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("sandbox: %s: %w", name, ctx.Err())
	}
	res := &Result{
		Stdout:    stdout.buf.String(),
		Stderr:    stderr.buf.String(),
		Truncated: stdout.truncated || stderr.truncated,
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		return nil, fmt.Errorf("sandbox: running %s: %w", name, err)
	}
	return res, nil
}

// cappedBuffer keeps the first limit bytes and discards the rest. It never
// fails a write, so the child process is not killed by a broken pipe.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := c.limit - c.buf.Len(); remaining < len(p) {
		c.truncated = true
		p = p[:max(remaining, 0)]
	}
	c.buf.Write(p)
	return len(p), nil
}
