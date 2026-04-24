//go:build livepg

// Package handlers — add_window_chars_livepg_test.go: end-to-end test for the
// M7 Stream C window_chars parameter against a live Postgres.
//
// What this exercises (top-to-bottom):
//   1. nativeFastAddForCube called twice on the same conversation:
//      first with WindowChars=nil (default 4096), then with WindowChars=512.
//   2. Both runs use a stub embedder (zero vectors) so cosine dedup never trips.
//   3. After each run we count Memory rows owned by the cube via
//      Postgres.CountRawMemories — the smaller window must produce strictly
//      more rows than the default.
//
// Gating:
//   - build tag `livepg` keeps this file out of `go test ./...` in CI.
//   - MEMDB_LIVE_PG_DSN must be set or the test t.Skip's. Format:
//     postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./internal/handlers/... \
//	    -run TestLivePG_WindowChars_FastAdd -count=1 -v
//
// Why a stub embedder:
//   The window-count assertion does not depend on retrieval quality. Using
//   real ONNX would add ~2GB RAM + 10s init for zero test signal.
//
// Cleanup:
//   Each cube_id is unique per test run; deferred delete removes the rows we
//   wrote, so the test never leaves the live DB dirty.

package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// openLivePGForWindowChars opens MEMDB_LIVE_PG_DSN — twin of openLivePGForChat,
// kept local so each _test.go file is self-contained.
func openLivePGForWindowChars(ctx context.Context, t *testing.T, logger *slog.Logger) *db.Postgres {
	t.Helper()
	dsn := os.Getenv("MEMDB_LIVE_PG_DSN")
	if dsn == "" {
		t.Skip("MEMDB_LIVE_PG_DSN not set; skipping live-Postgres window_chars test")
	}
	pg, err := db.NewPostgres(ctx, dsn, logger)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	return pg
}

// cleanupWindowCharsCube wipes every Memory row owned by cubeID. Mirrors the
// scheduler-package cleanupLivepgCube but trimmed to what the fast-add path
// actually writes (no graph edges, no tree_consolidation_log).
func cleanupWindowCharsCube(ctx context.Context, t *testing.T, pg *db.Postgres, cubeID string) {
	t.Helper()
	pool := pg.Pool()
	if pool == nil {
		return
	}
	const delMemory = `DELETE FROM memos_graph."Memory" WHERE properties->>('user_name'::text) = $1`
	if _, err := pool.Exec(ctx, delMemory, cubeID); err != nil {
		t.Logf("livepg cleanup Memory (cube=%s): %v", cubeID, err)
	}
}

// buildSyntheticMessages returns alternating user/assistant turns of short,
// distinct content so each message falls well under any tested window size,
// and so windows produce LongTermMemory rows that match CountRawMemories'
// "contains assistant turn" predicate.
func buildSyntheticMessages(numMessages int) []chatMessage {
	msgs := make([]chatMessage, numMessages)
	for i := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs[i] = chatMessage{
			Role:     role,
			Content:  fmt.Sprintf("memory entry number %d about topic %d", i, i%5),
			ChatTime: "2026-04-25T10:00:00",
		}
	}
	return msgs
}

func TestLivePG_WindowChars_FastAdd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg := openLivePGForWindowChars(ctx, t, logger)
	defer pg.Close()

	// Two distinct cubes so the runs do not interfere with each other's
	// CountRawMemories totals.
	defaultCube := fmt.Sprintf("livepg-window-default-%d", time.Now().UnixNano())
	smallCube := fmt.Sprintf("livepg-window-small-%d", time.Now().UnixNano())
	defer cleanupWindowCharsCube(ctx, t, pg, defaultCube)
	defer cleanupWindowCharsCube(ctx, t, pg, smallCube)

	h := &Handler{
		logger:   logger,
		postgres: pg,
		embedder: &stubEmbedder{},
	}

	msgs := buildSyntheticMessages(60)

	// --- Run 1: default window (WindowChars nil → 4096) ---
	defaultReq := &fullAddRequest{
		UserID:          strPtr(defaultCube),
		Mode:            strPtr(modeFast),
		Messages:        msgs,
		WritableCubeIDs: []string{defaultCube},
	}
	defaultItems, err := h.nativeFastAddForCube(ctx, defaultReq, defaultCube)
	if err != nil {
		t.Fatalf("default fast-add failed: %v", err)
	}
	defaultCount, err := pg.CountRawMemories(ctx, defaultCube)
	if err != nil {
		t.Fatalf("count default cube: %v", err)
	}

	// --- Run 2: small window (WindowChars=512) ---
	smallSize := 512
	smallReq := &fullAddRequest{
		UserID:          strPtr(smallCube),
		Mode:            strPtr(modeFast),
		WindowChars:     &smallSize,
		Messages:        msgs,
		WritableCubeIDs: []string{smallCube},
	}
	smallItems, err := h.nativeFastAddForCube(ctx, smallReq, smallCube)
	if err != nil {
		t.Fatalf("small fast-add failed: %v", err)
	}
	smallCount, err := pg.CountRawMemories(ctx, smallCube)
	if err != nil {
		t.Fatalf("count small cube: %v", err)
	}

	t.Logf("default windowChars=%d → %d items, %d rows in Memory",
		windowChars, len(defaultItems), defaultCount)
	t.Logf("override windowChars=%d → %d items, %d rows in Memory",
		smallSize, len(smallItems), smallCount)

	if defaultCount == 0 {
		t.Fatalf("default windowChars produced 0 Memory rows; expected ≥1")
	}
	if smallCount <= defaultCount {
		t.Errorf("WindowChars=512 produced %d rows, expected strictly more than default %d",
			smallCount, defaultCount)
	}

	// Sanity: a few items came back and contain expected content.
	if len(smallItems) < 6 {
		t.Errorf("expected ≥6 add response items for WindowChars=512, got %d", len(smallItems))
	}
	if len(smallItems) > 0 && !strings.Contains(smallItems[0].Memory, "memory entry number") {
		t.Errorf("first small-window memory missing expected token, got: %q", smallItems[0].Memory)
	}
}
