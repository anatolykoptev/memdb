package scheduler

// reorganizer_nodes_test.go — unit tests for cache invalidation in node removal helpers.

import (
	"context"
	"testing"
)

// capturingInvalidator records all patterns passed to Invalidate.
type capturingInvalidator struct {
	calls [][]string // each element is the patterns slice from one Invalidate call
}

func (c *capturingInvalidator) Invalidate(_ context.Context, patterns ...string) {
	c.calls = append(c.calls, patterns)
}

func (c *capturingInvalidator) totalPatterns() []string {
	var all []string
	for _, call := range c.calls {
		all = append(all, call...)
	}
	return all
}

func containsPattern(patterns []string, want string) bool {
	for _, p := range patterns {
		if p == want {
			return true
		}
	}
	return false
}

func TestInvalidateCaches_CallsInvalidatorWithCorrectPatterns(t *testing.T) {
	inv := &capturingInvalidator{}
	r := &Reorganizer{cacheInvalidator: inv}

	r.invalidateCaches(context.Background(), "test.com", "mem-abc")

	patterns := inv.totalPatterns()
	if len(patterns) == 0 {
		t.Fatal("expected Invalidate to be called, got 0 patterns")
	}
	wantSearch := "memdb:db:search:*:test.com:*"
	wantMemory := "memdb:db:memory:mem-abc"
	if !containsPattern(patterns, wantSearch) {
		t.Errorf("missing search pattern %q, got: %v", wantSearch, patterns)
	}
	if !containsPattern(patterns, wantMemory) {
		t.Errorf("missing memory pattern %q, got: %v", wantMemory, patterns)
	}
}

func TestInvalidateCaches_NoopWhenNilInvalidator(t *testing.T) {
	r := &Reorganizer{cacheInvalidator: nil}
	// Must not panic.
	r.invalidateCaches(context.Background(), "cube", "mem-id")
}
