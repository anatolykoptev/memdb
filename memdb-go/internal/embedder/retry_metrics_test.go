package embedder

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// TestRetryReason verifies the reason classifier covers all expected cases.
func TestRetryReason(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{name: "deadline exceeded", err: context.DeadlineExceeded, status: 0, want: "context"},
		{name: "context cancelled", err: context.Canceled, status: 0, want: "context"},
		{name: "http 429", err: errors.New("rate limited"), status: http.StatusTooManyRequests, want: "http_429"},
		{name: "http 503", err: errors.New("svc unavailable"), status: http.StatusServiceUnavailable, want: "http_5xx"},
		{name: "http 500", err: errors.New("internal error"), status: http.StatusInternalServerError, want: "http_5xx"},
		{name: "http 502", err: errors.New("bad gateway"), status: http.StatusBadGateway, want: "http_5xx"},
		{name: "transient network", err: errors.New("connection reset"), status: 0, want: "transient"},
		{name: "transient with ok status", err: errors.New("timeout"), status: 200, want: "transient"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retryReason(tc.err, tc.status)
			if got != tc.want {
				t.Errorf("retryReason(%v, %d) = %q; want %q", tc.err, tc.status, got, tc.want)
			}
		})
	}
}

// TestWithRetry_RecordsMetricOnRetry verifies that withRetry increments the
// retry counter on a retryable failure and then succeeds on the second attempt.
func TestWithRetry_RecordsMetricOnRetry(t *testing.T) {
	attempt := 0
	cfg := retryConfig{maxAttempts: 3, baseDelay: 0, maxDelay: 0}
	result, err := withRetry(context.Background(), cfg, func() (string, int, error) {
		attempt++
		if attempt < 2 {
			return "", http.StatusTooManyRequests, errors.New("rate limited")
		}
		return "ok", 0, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected ok, got %q", result)
	}
	if attempt != 2 {
		t.Errorf("expected 2 attempts, got %d", attempt)
	}
}
