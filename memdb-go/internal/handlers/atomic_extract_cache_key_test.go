package handlers

import (
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// TestAtomicCacheKey_DiffersByModel verifies a model swap invalidates a
// cached entry. Without folding modelName into the SHA input, two callers
// with the same conversation/cube/prompt but different LLMs would silently
// share extracted facts produced by the previous model.
func TestAtomicCacheKey_DiffersByModel(t *testing.T) {
	t.Parallel()
	args := func(model string) string {
		return computeAtomicCacheKey(
			"cube-1", "2026-05-02", "Alice met Bob.", []llm.Candidate{}, "PROMPT", model,
		)
	}
	a := args("model-a")
	b := args("model-b")
	if a == b {
		t.Fatalf("expected different keys for different model, got identical %s", a)
	}
}
