// Package handlers — add_cancel_test.go: BUG D test.
//
// A caller-cancelled request (context.Canceled) was classified/logged as a
// hard failure (ERROR level + outcome="error"). BUG D: classify it distinctly
// — Info log, outcome="canceled", counted separately from real errors.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestAddErrorOutcome(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "success"},
		{"context canceled", context.Canceled, "canceled"},
		{"context canceled wrapped", fmt.Errorf("batch embed: %w", context.Canceled), "canceled"},
		{"context deadline exceeded", context.DeadlineExceeded, "timeout"},
		{"context deadline wrapped", fmt.Errorf("batch embed: %w", context.DeadlineExceeded), "timeout"},
		{"real error", errors.New("postgres connection refused"), "error"},
		{"real error wrapped", fmt.Errorf("insert nodes: %w", errors.New("duplicate key")), "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := addErrorOutcome(tc.err)
			if got != tc.want {
				t.Errorf("addErrorOutcome(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
