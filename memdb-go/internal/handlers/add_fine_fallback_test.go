package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fineFallbackReason(tc.err); got != tc.want {
				t.Errorf("fineFallbackReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
