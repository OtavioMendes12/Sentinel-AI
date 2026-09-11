package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newRunner(t *testing.T, maxOutput int) *Runner {
	t.Helper()
	r, err := New(t.TempDir(), maxOutput, "sh", "env")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunRejectsUnlistedCommands(t *testing.T) {
	t.Parallel()

	if _, err := newRunner(t, 1024).Run(context.Background(), "rm", "-rf", "."); err == nil {
		t.Fatal("expected unlisted command to be refused")
	}
	if _, err := New(t.TempDir(), 1024, "/bin/sh"); err == nil {
		t.Fatal("expected a path to be refused as a command name")
	}
	if _, err := New(t.TempDir(), 1024, "definitely-not-a-real-binary"); err == nil {
		t.Fatal("expected missing binary to fail at startup")
	}
}

// Not parallel: it sets a process environment variable.
func TestRunScrubsEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak-0000")

	res, err := newRunner(t, 4096).Run(context.Background(), "env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "must-not-leak") || strings.Contains(res.Stdout, "OPENAI_API_KEY") {
		t.Fatalf("parent environment reached the child:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "PATH=") {
		t.Fatalf("expected a minimal PATH:\n%s", res.Stdout)
	}
}

func TestRunDoesNotUseAShell(t *testing.T) {
	t.Parallel()

	// With a shell, $(...) would run; as a plain argument it is printed verbatim.
	res, err := newRunner(t, 1024).Run(context.Background(), "sh", "-c", `printf '%s' "$1"`, "_", "$(echo injected)")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "$(echo injected)" {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestRunReportsExitCodeAndCapsOutput(t *testing.T) {
	t.Parallel()

	res, err := newRunner(t, 10).Run(context.Background(), "sh", "-c", "printf 'aaaaaaaaaaaaaaaaaaaa'; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.Stdout != "aaaaaaaaaa" || !res.Truncated {
		t.Fatalf("got %+v", res)
	}
}

func TestRunKillsProcessGroupOnTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	// The background child keeps the output pipe open; the group kill must end it too.
	_, err := newRunner(t, 1024).Run(ctx, "sh", "-c", "sleep 30 & sleep 30")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run returned after %v; children were not killed", elapsed)
	}
}
