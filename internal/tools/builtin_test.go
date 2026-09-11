package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OtavioMendes12/Sentinel-AI/internal/domain"
	"github.com/OtavioMendes12/Sentinel-AI/internal/sandbox"
)

// newRepo creates a fake repository checkout with the given files.
func newRepo(t *testing.T, files map[string]string) (string, *Workspace) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := OpenWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return dir, ws
}

func invoke(t *testing.T, tool Tool, args string) (string, error) {
	t.Helper()
	if err := tool.InputSchema.Validate([]byte(args)); err != nil {
		t.Fatalf("test arguments do not match the %s schema: %v", tool.Name, err)
	}
	return tool.Handler(context.Background(), json.RawMessage(args))
}

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestBuiltinToolsRegister(t *testing.T) {
	t.Parallel()

	_, ws := newRepo(t, nil)
	reg := NewRegistry()
	for _, tool := range []Tool{NewReadFile(ws), NewSearchCode(ws, nil), NewGetDiff(nil)} {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		if tool.Risk != RiskRead {
			t.Errorf("%s must be read-only", tool.Name)
		}
	}
}

func TestReadFile(t *testing.T) {
	t.Parallel()

	_, ws := newRepo(t, map[string]string{
		"internal/user/service.go": numbered(10),
		"big.txt":                  numbered(maxReadLines + 50),
		"empty.txt":                "",
	})
	tool := NewReadFile(ws)

	out, err := invoke(t, tool, `{"path":"internal/user/service.go","start_line":3,"end_line":4}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "internal/user/service.go (lines 3-4 of 10)\n     3  line 3\n     4  line 4\n"; out != want {
		t.Errorf("got %q, want %q", out, want)
	}

	out, err = invoke(t, tool, `{"path":"./big.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, fmt.Sprintf("big.txt (lines 1-%d of %d)", maxReadLines, maxReadLines+50)) ||
		!strings.Contains(out, fmt.Sprintf("start_line=%d", maxReadLines+1)) {
		t.Errorf("large file not paginated: %.80q", out)
	}

	if out, err := invoke(t, tool, `{"path":"empty.txt"}`); err != nil || !strings.Contains(out, "empty file") {
		t.Errorf("empty file: %q, %v", out, err)
	}
}

func TestReadFileErrors(t *testing.T) {
	t.Parallel()

	dir, ws := newRepo(t, map[string]string{
		"main.go":        "package main\n",
		"logo.png":       "\x89PNG\x00\x00",
		".git/config":    "[remote]\n",
		".env":           "DATABASE_PASSWORD=placeholder\n",
		".env.local":     "X=1\n",
		".env.example":   "X=\n",
		"certs/tls.pem":  "cert",
		"deploy/id_rsa":  "key",
		"docs/guide.txt": "hello\n",
		"docs/five.txt":  numbered(5),
	})
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"notes.txt":    ".git/config", // points inside the repo, at a blocked file
		"escape.txt":   outside,       // points outside the repo
		"docs/linkdir": "..",          // directory link
	} {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewReadFile(ws)

	tests := map[string]struct{ args, want string }{
		"traversal":            {`{"path":"../outside.txt"}`, "stay inside the repository"},
		"nested traversal":     {`{"path":"docs/../../x"}`, "stay inside the repository"},
		"absolute":             {`{"path":"/etc/passwd"}`, "relative to the repository root"},
		"backslash":            {`{"path":"docs\\guide.txt"}`, "slash-separated"},
		"git internals":        {`{"path":".git/config"}`, "not allowed"},
		"dotenv":               {`{"path":".env"}`, "not allowed"},
		"dotenv variant":       {`{"path":".env.local"}`, "not allowed"},
		"private key ext":      {`{"path":"certs/tls.pem"}`, "not allowed"},
		"ssh key":              {`{"path":"deploy/id_rsa"}`, "not allowed"},
		"symlink to blocked":   {`{"path":"notes.txt"}`, "symbolic link"},
		"symlink outside":      {`{"path":"escape.txt"}`, "symbolic link"},
		"through dir symlink":  {`{"path":"docs/linkdir/main.go"}`, "symbolic link"},
		"missing":              {`{"path":"nope.go"}`, "does not exist"},
		"directory":            {`{"path":"docs"}`, "is a directory"},
		"binary":               {`{"path":"logo.png"}`, "binary file"},
		"start past end":       {`{"path":"main.go","start_line":5}`, "past the end"},
		"end before start":     {`{"path":"docs/five.txt","start_line":4,"end_line":2}`, "before start_line"},
		"empty path":           {`{"path":" "}`, "path is required"},
		"example is permitted": {`{"path":".env.example"}`, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, err := invoke(t, tool, tt.args)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got (%q, %v), want error containing %q", out, err, tt.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaks file content: %v", err)
			}
		})
	}
}

func TestGetDiff(t *testing.T) {
	t.Parallel()

	tool := NewGetDiff([]domain.ChangedFile{
		{Path: "main.go", Status: "modified", Additions: 2, Deletions: 1, Patch: "@@ -1 +1,2 @@\n-a\n+b\n+c"},
		{Path: "new.go", PreviousPath: "old.go", Status: "renamed"},
	})

	out, err := invoke(t, tool, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 changed files", "modified main.go (+2 -1)", "new.go (+0 -0) renamed from old.go [no textual diff]"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q:\n%s", want, out)
		}
	}

	if out, err := invoke(t, tool, `{"path":"./main.go"}`); err != nil || !strings.Contains(out, "+++ b/main.go\n@@ -1 +1,2 @@") {
		t.Errorf("per-file diff: %q, %v", out, err)
	}
	if out, err := invoke(t, tool, `{"path":"old.go"}`); err != nil || !strings.Contains(out, "rename from old.go") {
		t.Errorf("lookup by previous path: %q, %v", out, err)
	}
	if _, err := invoke(t, tool, `{"path":"other.go"}`); err == nil || !strings.Contains(err.Error(), "not changed by this pull request") {
		t.Errorf("unchanged file: %v", err)
	}
}

func TestSearchCode(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) is not installed")
	}

	dir, ws := newRepo(t, map[string]string{
		"internal/user/service.go":      "func (s *Service) Save(ctx context.Context) error {\n\treturn s.repo.Save(ctx)\n}\n",
		"internal/user/service_test.go": "func TestSave(t *testing.T) { s.Save(ctx) }\n",
		"cmd/main.go":                   "svc.Save(ctx) // a.b\n",
		".github/workflows/ci.yml":      "run: Save\n",
		"certs/server.key":              "Save private material\n",
		".env.local":                    "Save=1\n",
	})
	runner, err := sandbox.New(dir, 64<<10, "rg")
	if err != nil {
		t.Fatal(err)
	}
	tool := NewSearchCode(ws, runner)

	out, err := invoke(t, tool, `{"query":"Save("}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"internal/user/service.go:1:", "internal/user/service_test.go:1:", "cmd/main.go:1:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	for _, blocked := range []string{"server.key", ".env.local", "./"} {
		if strings.Contains(out, blocked) {
			t.Errorf("output contains %q:\n%s", blocked, out)
		}
	}

	if out, _ := invoke(t, tool, `{"query":"a.b"}`); !strings.Contains(out, "1 matching lines") {
		t.Errorf("fixed-string search should match the dot literally:\n%s", out)
	}
	if out, _ := invoke(t, tool, `{"query":"func \\(s \\*Service\\) \\w+","regex":true}`); !strings.Contains(out, "service.go:1:") {
		t.Errorf("regex search:\n%s", out)
	}
	if out, _ := invoke(t, tool, `{"query":"Save","path":"cmd"}`); strings.Contains(out, "internal/") {
		t.Errorf("path restriction ignored:\n%s", out)
	}
	if out, _ := invoke(t, tool, `{"query":"Save","path":".github"}`); !strings.Contains(out, ".github/workflows/ci.yml:1:") {
		t.Errorf("explicit hidden directory path:\n%s", out)
	}
	if out, _ := invoke(t, tool, `{"query":"Save","max_results":1}`); !strings.Contains(out, "showing the first 1") {
		t.Errorf("max_results not applied:\n%s", out)
	}
	if out, _ := invoke(t, tool, `{"query":"nothing-matches-this"}`); !strings.Contains(out, "no matches") {
		t.Errorf("no-match result:\n%s", out)
	}
	if _, err := invoke(t, tool, `{"query":"(","regex":true}`); err == nil || !strings.Contains(err.Error(), "search failed") {
		t.Errorf("invalid regex: %v", err)
	}
	if _, err := invoke(t, tool, `{"query":"Save","path":"../"}`); err == nil {
		t.Error("traversal in path must be refused")
	}
}

func TestSearchCodeCannotInjectFlags(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) is not installed")
	}

	dir, ws := newRepo(t, map[string]string{"main.go": "package main\n"})
	if err := os.MkdirAll(filepath.Join(dir, "--pre=touch"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := sandbox.New(dir, 64<<10, "rg")
	if err != nil {
		t.Fatal(err)
	}
	tool := NewSearchCode(ws, runner)
	marker := filepath.Join(dir, "pwned")

	// --pre makes ripgrep execute a program per file. As a query or path it must stay inert.
	_, _ = invoke(t, tool, `{"query":"--pre=touch `+marker+`"}`)
	_, _ = invoke(t, tool, `{"query":"package","path":"--pre=touch"}`)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a flag was injected into ripgrep")
	}
}
