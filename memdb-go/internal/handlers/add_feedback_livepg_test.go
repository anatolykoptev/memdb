//go:build livepg

// Package handlers — add_feedback_livepg_test.go: handler-level end-to-end test
// for the M10 Stream 8 reward-scaffold feedback persistence.
//
// What this exercises:
//  1. POST /product/feedback succeeds via NativeFeedback.
//  2. After the request returns, a feedback_events row exists in the DB.
//  3. The row has correct user_id, query, and label fields.
//
// Gating:
//   - Build tag `livepg` keeps this file out of `go test ./...` in CI.
//   - MEMDB_LIVE_PG_DSN must be set or the test t.Skip's.
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./internal/handlers/... \
//	    -run TestLivePG_FeedbackPersistence -count=1 -v

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// openLivePGForFeedback opens MEMDB_LIVE_PG_DSN — kept local so each _test.go
// file is self-contained.
func openLivePGForFeedback(ctx context.Context, t *testing.T) *db.Postgres {
	t.Helper()
	dsn := os.Getenv("MEMDB_LIVE_PG_DSN")
	if dsn == "" {
		t.Skip("MEMDB_LIVE_PG_DSN not set; skipping live-Postgres feedback persistence test")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg, err := db.NewPostgres(ctx, dsn, logger)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	return pg
}

// TestLivePG_FeedbackPersistence hits NativeFeedback and verifies a row lands in
// memos_graph.feedback_events.
func TestLivePG_FeedbackPersistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg := openLivePGForFeedback(ctx, t)
	defer pg.Close()

	userID := fmt.Sprintf("test-feedback-handler-%d", time.Now().UnixNano())

	// Cleanup after test.
	t.Cleanup(func() {
		_, _ = pg.Pool().Exec(context.Background(),
			`DELETE FROM memos_graph.feedback_events WHERE user_id = $1`, userID)
	})

	// Build a minimal stub LLM server (NativeFeedback requires llmChat != nil).
	stubLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "[]"}},
			},
		})
	}))
	defer stubLLM.Close()

	h := &Handler{
		logger:   logger,
		postgres: pg,
		embedder: &stubEmbedder{},
		llmChat:  llm.NewClient(stubLLM.URL, "test-key", "stub-model", logger),
	}

	body, _ := json.Marshal(map[string]any{
		"user_id":          userID,
		"feedback_content": "My name is Alex",
		"history":          []any{},
	})

	req := httptest.NewRequest(http.MethodPost, "/product/feedback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.NativeFeedback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("NativeFeedback returned %d: %s", w.Code, w.Body.String())
	}

	// persistFeedbackEvent is fire-and-forget in a goroutine.
	// Poll briefly to let the write complete.
	var events []db.FeedbackEvent
	var pollErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, pollErr = pg.GetFeedbackEventsByUser(ctx, userID)
		if pollErr == nil && len(events) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if pollErr != nil {
		t.Fatalf("GetFeedbackEventsByUser: %v", pollErr)
	}
	if len(events) == 0 {
		t.Fatal("expected ≥1 feedback_events row after NativeFeedback; got 0 — handler may not be wired")
	}

	ev := events[0]
	if ev.UserID != userID {
		t.Errorf("user_id: got %q want %q", ev.UserID, userID)
	}
	if ev.Query != "My name is Alex" {
		t.Errorf("query: got %q want %q", ev.Query, "My name is Alex")
	}
	if ev.Label != "neutral" {
		t.Errorf("label: got %q want neutral", ev.Label)
	}
}
