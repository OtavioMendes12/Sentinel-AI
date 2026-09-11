package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var fastPolicy = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond}

func serve(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(hits.Add(1))
		status := statuses[min(n, len(statuses))-1]
		w.WriteHeader(status)
		_, _ = w.Write([]byte(http.StatusText(status)))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func get(url string) func(context.Context) (*http.Request, error) {
	return func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	}
}

// do calls Do and closes any successful response.
func do(ctx context.Context, srv *httptest.Server, p RetryPolicy) error {
	resp, err := Do(ctx, srv.Client(), p, get(srv.URL))
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

func TestDoRetriesTransientFailures(t *testing.T) {
	t.Parallel()

	srv, hits := serve(t, 503, 429, 200)
	if err := do(context.Background(), srv, fastPolicy); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits.Load() != 3 {
		t.Errorf("hits = %d, want 3", hits.Load())
	}
}

func TestDoDoesNotRetryClientErrors(t *testing.T) {
	t.Parallel()

	srv, hits := serve(t, 401)
	err := do(context.Background(), srv, fastPolicy)
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != 401 || statusErr.Body != "Unauthorized" {
		t.Fatalf("err = %v, want StatusError 401", err)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", hits.Load())
	}
}

func TestDoGivesUp(t *testing.T) {
	t.Parallel()

	srv, hits := serve(t, 502)
	err := do(context.Background(), srv, fastPolicy)
	if err == nil || !strings.Contains(err.Error(), "giving up after 3 attempts") {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 3 {
		t.Errorf("hits = %d, want 3", hits.Load())
	}
}

func TestDoStopsWhenContextEnds(t *testing.T) {
	t.Parallel()

	srv, _ := serve(t, 503)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	slow := RetryPolicy{MaxAttempts: 5, BaseDelay: time.Hour}
	if err := do(ctx, srv, slow); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestDelay(t *testing.T) {
	t.Parallel()

	p := RetryPolicy{BaseDelay: time.Second, MaxDelay: 5 * time.Second}
	if got := delay(p, 1, 0); got != time.Second {
		t.Errorf("attempt 1: %v", got)
	}
	if got := delay(p, 3, 0); got != 4*time.Second {
		t.Errorf("attempt 3: %v", got)
	}
	if got := delay(p, 1, 2*time.Minute); got != 5*time.Second {
		t.Errorf("Retry-After must be capped: %v", got)
	}
	if parseRetryAfter("7") != 7*time.Second || parseRetryAfter("soon") != 0 {
		t.Error("parseRetryAfter")
	}
}

func TestReadLimited(t *testing.T) {
	t.Parallel()

	if _, err := ReadLimited(strings.NewReader("12345"), 5); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	if _, err := ReadLimited(strings.NewReader("123456"), 5); err == nil {
		t.Error("expected error above the limit")
	}
}
