// Package httpx provides the HTTP plumbing shared by the API clients:
// bounded retries for transient failures and size-limited error bodies.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy controls retries of transient failures: network errors and
// HTTP 429/500/502/503/504. Other statuses are returned immediately, since
// retrying a bad request or an authorization failure cannot succeed.
type RetryPolicy struct {
	MaxAttempts int           // total attempts, including the first
	BaseDelay   time.Duration // doubled after each attempt
	MaxDelay    time.Duration // cap for computed and Retry-After delays
}

// DefaultRetryPolicy suits interactive API calls within a CI job.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: 30 * time.Second}

// maxErrorBody bounds how much of an error response is kept.
const maxErrorBody = 4 << 10

// StatusError is returned for non-2xx responses.
type StatusError struct {
	StatusCode int
	Body       string // truncated response body
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("unexpected HTTP status %d: %s", e.StatusCode, e.Body)
}

// Do sends the request built by newRequest, retrying transient failures.
// newRequest is called once per attempt so request bodies can be replayed.
// On success the caller owns the response body; non-2xx responses are
// consumed and returned as *StatusError.
func Do(ctx context.Context, client *http.Client, policy RetryPolicy, newRequest func(context.Context) (*http.Request, error)) (*http.Response, error) {
	attempts := max(policy.MaxAttempts, 1)
	var lastErr error

	for attempt := 1; ; attempt++ {
		req, err := newRequest(ctx)
		if err != nil {
			return nil, fmt.Errorf("building request: %w", err)
		}

		resp, err := client.Do(req) //nolint:gosec // URLs come from validated configuration, not from users
		var retryAfter time.Duration
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return resp, nil
		default:
			lastErr = statusError(resp)
			if !retryable(resp.StatusCode) {
				return nil, lastErr
			}
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}

		if attempt >= attempts {
			return nil, fmt.Errorf("giving up after %d attempts: %w", attempt, lastErr)
		}
		if err := sleep(ctx, delay(policy, attempt, retryAfter)); err != nil {
			return nil, err
		}
	}
}

func statusError(resp *http.Response) *StatusError {
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return &StatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
}

func retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func delay(p RetryPolicy, attempt int, retryAfter time.Duration) time.Duration {
	d := retryAfter
	if d == 0 {
		d = p.BaseDelay << (attempt - 1)
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
	}
	return d
}

// parseRetryAfter supports the delay-seconds form, which is what GitHub and
// OpenAI send.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ReadLimited reads at most limit bytes from r and fails if there is more,
// so a misbehaving server cannot exhaust memory.
func ReadLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response body exceeds size limit")
	}
	return data, nil
}
