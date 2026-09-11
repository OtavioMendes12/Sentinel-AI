package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OtavioMendes12/Sentinel-AI/internal/domain"
	"github.com/OtavioMendes12/Sentinel-AI/internal/httpx"
)

const testToken = "test-github-token-0000"

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL+"/", testToken, srv.Client())
	c.retry = httpx.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond}
	return c
}

func checkHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got)
	}
	if r.Header.Get("X-GitHub-Api-Version") != apiVersion || r.Header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("missing API headers: %v", r.Header)
	}
}

func TestPullRequest(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		if r.URL.Path != "/repos/example-org/example-repository/pulls/42" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"number":42,"title":"Add cache","body":"Adds a cache.","state":"open","draft":false,
			"base":{"sha":"base111"},"head":{"sha":"head222"},"user":{"login":"john-doe"}}`))
	})

	pr, err := c.PullRequest(context.Background(), "example-org/example-repository", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := domain.PullRequest{Repository: "example-org/example-repository", Number: 42, Title: "Add cache",
		Body: "Adds a cache.", State: "open", BaseSHA: "base111", HeadSHA: "head222"}
	if *pr != want {
		t.Errorf("got %+v, want %+v", *pr, want)
	}
}

func TestPullRequestFilesPaginates(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		n := filesPerPage
		if page == 2 {
			n = 3
		}
		files := make([]fileResponse, n)
		for i := range files {
			files[i] = fileResponse{Filename: fmt.Sprintf("p%d/f%d.go", page, i), Status: "modified", Patch: "@@ -1 +1 @@\n-a\n+b"}
		}
		_ = json.NewEncoder(w).Encode(files)
	})

	files, err := c.PullRequestFiles(context.Background(), "example-org/example-repository", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != filesPerPage+3 || files[filesPerPage].Path != "p2/f0.go" {
		t.Fatalf("got %d files", len(files))
	}
}

func TestPullRequestFilesDropsOversizedPatches(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]fileResponse{{Filename: "big.sql", Status: "added", Patch: strings.Repeat("+x\n", maxPatchBytes)}})
	})
	files, err := c.PullRequestFiles(context.Background(), "example-org/example-repository", 1)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Patch != "" {
		t.Error("oversized patch should be dropped")
	}
}

func TestErrorsDoNotLeakToken(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	_, err := c.PullRequest(context.Background(), "example-org/example-repository", 7)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestRepoPathEscapes(t *testing.T) {
	t.Parallel()

	if got := repoPath("example-org/example-repository", "pulls", "1"); got != "/repos/example-org/example-repository/pulls/1" {
		t.Errorf("got %s", got)
	}
	if got := repoPath("a/b?x=1", "pulls"); got != "/repos/a/b%3Fx=1/pulls" {
		t.Errorf("got %s", got)
	}
}

func TestRenderDiff(t *testing.T) {
	t.Parallel()

	diff := RenderDiff([]domain.ChangedFile{
		{Path: "main.go", Status: "modified", Patch: "@@ -1 +1 @@\n-a\n+b\n"},
		{Path: "new.go", PreviousPath: "old.go", Status: "renamed", Patch: "@@ -1 +1 @@\n-x\n+y"},
		{Path: "logo.png", Status: "added"},
	})
	for _, want := range []string{
		"diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-a\n+b\n",
		"rename from old.go\nrename to new.go\n--- a/old.go\n+++ b/new.go\n",
		"diff --git a/logo.png b/logo.png\nnew file\n(no textual diff",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
}
