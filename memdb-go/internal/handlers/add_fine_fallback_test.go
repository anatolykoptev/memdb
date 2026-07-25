package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

func TestShouldFallbackToFast(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadline exceeded direct", context.DeadlineExceeded, true},
		{"deadline exceeded wrapped", fmt.Errorf("atomic extract: %w", context.DeadlineExceeded), true},
		{"context canceled", context.Canceled, true},
		{"context canceled wrapped", fmt.Errorf("oops: %w", context.Canceled), true},
		{"deadline string match", errors.New("rpc: context deadline exceeded after 60s"), true},
		{"circuit breaker open", errors.New("circuit breaker open"), true},
		{"circuit_open underscore", errors.New("ce_circuit_open=true"), true},
		// BUG A: LLM HTTP status errors (429/5xx) must trigger the fine→fast
		// degraded fallback so the caller's memory is persisted in embed-only
		// form instead of dropped. Pre-fix these returned false (data loss).
		{"api error 429", &llm.APIError{StatusCode: 429, Message: "rate limit"}, true},
		{"api error 429 wrapped", fmt.Errorf("atomic fine add: extract: %w", &llm.APIError{StatusCode: 429, Message: "rate limit"}), true},
		{"api error 502", &llm.APIError{StatusCode: 502, Message: "bad gateway"}, true},
		{"api error 503", &llm.APIError{StatusCode: 503, Message: "unavailable"}, true},
		{"status error 429", &llm.StatusError{Code: 429, Retries: 1}, true},
		{"status error 429 wrapped", fmt.Errorf("atomic extract: %w", &llm.StatusError{Code: 429, Retries: 1}), true},
		{"status error 502", &llm.StatusError{Code: 502}, true},
		// 4xx (not 429) is a client error — NOT a fallback candidate.
		{"api error 400", &llm.APIError{StatusCode: 400, Message: "bad request"}, false},
		{"api error 401", &llm.APIError{StatusCode: 401, Message: "unauthorized"}, false},
		{"sql error not retryable", errors.New("constraint violation: duplicate key"), false},
		{"validation error not retryable", errors.New("invalid request: missing user_id"), false},
		{"parse error not retryable", errors.New("json: cannot unmarshal"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldFallbackToFast(tc.err); got != tc.want {
				t.Errorf("shouldFallbackToFast(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestFineFallbackReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "unknown"},
		{"deadline", context.DeadlineExceeded, "llm_timeout"},
		{"deadline wrapped", fmt.Errorf("x: %w", context.DeadlineExceeded), "llm_timeout"},
		{"canceled", context.Canceled, "canceled"},
		{"deadline string", errors.New("context deadline exceeded"), "llm_timeout"},
		{"circuit", errors.New("circuit breaker open"), "circuit_open"},
		{"unknown llm", errors.New("rate limit hit"), "llm_error"},
		// BUG A: typed LLM status errors map to "llm_error".
		{"api error 429", &llm.APIError{StatusCode: 429, Message: "rate limit"}, "llm_error"},
		{"api error 502", &llm.APIError{StatusCode: 502, Message: "bad gateway"}, "llm_error"},
		{"status error 429", &llm.StatusError{Code: 429, Retries: 1}, "llm_error"},
		{"status error 502", &llm.StatusError{Code: 502}, "llm_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fineFallbackReason(tc.err); got != tc.want {
				t.Errorf("fineFallbackReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
