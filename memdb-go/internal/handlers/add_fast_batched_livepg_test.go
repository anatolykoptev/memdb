//go:build livepg

// add_fast_batched_livepg_test.go — F2 latency regression test.
//
// What this exercises:
//   1. nativeFastAddForCube called once with WindowChars=512 on a 30-msg payload.
//   2. The 30-msg payload at window=512 historically produced ~24 windows ⇒
//      24 sequential ~550ms embed calls ⇒ ~13s p95 before F2.
//   3. With batched-embed enabled, the entire add path should complete in
//      well under 5s even with the stub embedder (which is essentially free).
//
// Why a 5s threshold:
//   The stub embedder returns instantly, so this test does NOT directly
//   measure the speedup vs the real ONNX embedder. It DOES catch any
//   regression where someone reintroduces a per-window sequential call
//   pattern (e.g. an extra Postgres round-trip per window) that would
//   reasonably exceed the budget. For real-embedder timings, see manual
//   probes documented in the F2 PR.
//
// Gating:
//   - build tag `livepg` keeps this out of `go test ./...` in CI.
//   - MEMDB_LIVE_PG_DSN must be set or the test t.Skip's.
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./internal/handlers/... \
//	    -run TestLivePG_FastAdd_BatchedLatency -count=1 -v
//
// Cleanup:
//   The test cube is unique per run; deferred delete removes the rows.

package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestLivePG_FastAdd_BatchedLatency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg := openLivePGForWindowChars(ctx, t, logger)
	defer pg.Close()

	cube := fmt.Sprintf("livepg-fastadd-batched-%d", time.Now().UnixNano())
	defer cleanupWindowCharsCube(ctx, t, pg, cube)

	emb := &recordingEmbedder{}
	h := &Handler{
		logger:   logger,
		postgres: pg,
		embedder: emb,
	}

	msgs := buildSyntheticMessages(30) // mirrors the payload size that exposed the cliff
	smallSize := 512
	req := &fullAddRequest{
		UserID:          strPtr(cube),
		Mode:            strPtr(modeFast),
		WindowChars:     &smallSize,
		Messages:        msgs,
		WritableCubeIDs: []string{cube},
	}

	start := time.Now()
	items, err := h.nativeFastAddForCube(ctx, req, cube)
	dur := time.Since(start)
	if err != nil {
		t.Fatalf("nativeFastAddForCube failed: %v", err)
	}
	calls := emb.calls.Load()

	t.Logf("fast-add batched: %d items in %s (Embed calls=%d, last batch size=%d)",
		len(items), dur, calls, len(emb.lastTexts))

	// Hard latency budget: 5s. With batched embed and a stub embedder this
	// should land in the low milliseconds; 5s leaves ample headroom for
	// Postgres jitter on the live DB while still catching a regression
	// to per-window sequential calls.
	if dur > 5*time.Second {
		t.Errorf("fast-add latency %s exceeds 5s budget — possible regression to sequential embed calls", dur)
	}

	// Defence-in-depth: with the stub embedder there's exactly one allowed
	// Embed call shape — at most 1 (zero is acceptable if all inputs got
	// hash-deduped, but for a fresh cube that's not the case).
	if calls != 1 {
		t.Errorf("expected exactly 1 batched Embed call for fresh cube, got %d", calls)
	}
}
