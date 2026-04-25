//go:build livepg

// Package search — cot_decomposer_livepg_test.go: end-to-end test for D11
// CoT decomposer against a real LLM endpoint (cliproxyapi at :8317 by
// convention).
//
// Gating:
//   - build tag `livepg` keeps this file out of `go test ./...` in CI.
//   - MEMDB_LIVE_LLM_URL must be set, e.g. http://127.0.0.1:8317.
//   - MEMDB_LIVE_LLM_MODEL is optional (defaults to gemini-2.5-flash-lite —
//     cheapest stable model that returns valid JSON arrays).
//   - MEMDB_LIVE_LLM_KEY is optional; cliproxyapi accepts unauthenticated
//     calls in dev.
//
// Invocation:
//
//	MEMDB_LIVE_LLM_URL=http://127.0.0.1:8317 \
//	GOWORK=off go test -tags=livepg ./internal/search/... \
//	    -run TestLiveLLM_CoTDecompose -count=1 -v

package search

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveLLM_CoTDecompose_MultiHop(t *testing.T) {
	url := os.Getenv("MEMDB_LIVE_LLM_URL")
	if url == "" {
		t.Skip("MEMDB_LIVE_LLM_URL not set; skipping live LLM test")
	}
	model := os.Getenv("MEMDB_LIVE_LLM_MODEL")
	if model == "" {
		model = "gemini-2.5-flash-lite"
	}
	d := NewCoTDecomposer(CoTDecomposerConfig{
		APIURL:        url,
		APIKey:        os.Getenv("MEMDB_LIVE_LLM_KEY"),
		Model:         model,
		Enabled:       true,
		MaxSubQueries: 3,
		Timeout:       10 * time.Second,
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	q := "What did Caroline do in Boston after she met Emma at the conference?"
	subs := d.Decompose(context.Background(), logger, q)
	if len(subs) < 2 {
		t.Fatalf("expected ≥2 sub-queries from real LLM, got %d: %v", len(subs), subs)
	}
	if subs[0] != q {
		t.Errorf("expected original at index 0, got %q", subs[0])
	}
	// Soft assertion: the LLM should mention either Caroline / Emma / Boston
	// in at least one of the sub-questions. We don't pin exact wording.
	joined := strings.ToLower(strings.Join(subs, " | "))
	for _, want := range []string{"caroline", "emma", "boston"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected sub-queries to mention %q, got %v", want, subs)
		}
	}
	t.Logf("sub-queries: %v", subs)
}
