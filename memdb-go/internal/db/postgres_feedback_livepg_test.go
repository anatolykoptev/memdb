//go:build livepg

package db_test

// postgres_feedback_livepg_test.go — end-to-end tests for feedback_events + extract_examples tables.
// Requires a live PostgreSQL instance with MemDB migrations applied.
// Run: MEMDB_TEST_POSTGRES_URL=<dsn> go test -tags=livepg ./memdb-go/internal/db/...

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// setupFeedbackTest returns a Postgres handle and a per-test user ID.
// Rows are cleaned up after the test.
func setupFeedbackTest(t *testing.T) (*db.Postgres, string) {
	t.Helper()
	pg, cleanup := setupCubesTest(t)
	ctx := context.Background()

	userID := "test-feedback-" + t.Name()

	if _, err := pg.Pool().Exec(ctx,
		`DELETE FROM memos_graph.feedback_events WHERE user_id = $1`, userID,
	); err != nil {
		t.Fatalf("cleanup prior feedback rows: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pg.Pool().Exec(context.Background(),
			`DELETE FROM memos_graph.feedback_events WHERE user_id = $1`, userID)
		cleanup()
	})
	return pg, userID
}

// --- InsertFeedbackEvent ---

func TestFeedbackEvents_InsertAndGetByUser(t *testing.T) {
	pg, userID := setupFeedbackTest(t)
	ctx := context.Background()

	ev, err := pg.InsertFeedbackEvent(ctx, db.InsertFeedbackEventParams{
		UserID:     userID,
		Query:      "what is my name?",
		Prediction: "Alex",
		Label:      "positive",
	})
	if err != nil {
		t.Fatalf("InsertFeedbackEvent: %v", err)
	}
	if ev.ID == 0 {
		t.Error("expected non-zero ID after insert")
	}
	if ev.UserID != userID {
		t.Errorf("UserID: got %q want %q", ev.UserID, userID)
	}
	if ev.Label != "positive" {
		t.Errorf("Label: got %q want positive", ev.Label)
	}
	if ev.CubeID != nil {
		t.Error("CubeID should be nil when not provided")
	}

	events, err := pg.GetFeedbackEventsByUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetFeedbackEventsByUser: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Query != "what is my name?" {
		t.Errorf("Query: got %q", events[0].Query)
	}
}

func TestFeedbackEvents_AllLabels(t *testing.T) {
	pg, userID := setupFeedbackTest(t)
	ctx := context.Background()

	labels := []string{"positive", "negative", "neutral", "correction"}
	for _, label := range labels {
		correction := "fixed answer"
		params := db.InsertFeedbackEventParams{
			UserID:     userID,
			Query:      "q",
			Prediction: "p",
			Label:      label,
		}
		if label == "correction" {
			params.Correction = &correction
		}
		ev, err := pg.InsertFeedbackEvent(ctx, params)
		if err != nil {
			t.Fatalf("InsertFeedbackEvent label=%q: %v", label, err)
		}
		if ev.Label != label {
			t.Errorf("label round-trip: got %q want %q", ev.Label, label)
		}
	}

	events, err := pg.GetFeedbackEventsByUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetFeedbackEventsByUser: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events (one per label), got %d", len(events))
	}
}

func TestFeedbackEvents_WithCubeID(t *testing.T) {
	pg, userID := setupFeedbackTest(t)
	ctx := context.Background()

	cubeID := "cube-test-001"
	ev, err := pg.InsertFeedbackEvent(ctx, db.InsertFeedbackEventParams{
		UserID:     userID,
		CubeID:     &cubeID,
		Query:      "query",
		Prediction: "pred",
		Label:      "neutral",
	})
	if err != nil {
		t.Fatalf("InsertFeedbackEvent with cube_id: %v", err)
	}
	if ev.CubeID == nil || *ev.CubeID != cubeID {
		t.Errorf("CubeID: got %v want %q", ev.CubeID, cubeID)
	}
}

func TestFeedbackEvents_OrderNewestFirst(t *testing.T) {
	pg, userID := setupFeedbackTest(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := pg.InsertFeedbackEvent(ctx, db.InsertFeedbackEventParams{
			UserID:     userID,
			Query:      "q",
			Prediction: "p",
			Label:      "neutral",
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	events, err := pg.GetFeedbackEventsByUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetFeedbackEventsByUser: %v", err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].CreatedAt.After(events[i-1].CreatedAt) {
			t.Error("events not ordered newest first")
		}
	}
}

// --- InsertExtractExample ---

func TestExtractExamples_InsertWithSourceEvent(t *testing.T) {
	pg, userID := setupFeedbackTest(t)
	ctx := context.Background()

	ev, err := pg.InsertFeedbackEvent(ctx, db.InsertFeedbackEventParams{
		UserID:     userID,
		Query:      "q",
		Prediction: "p",
		Label:      "correction",
	})
	if err != nil {
		t.Fatalf("InsertFeedbackEvent: %v", err)
	}

	gold, _ := json.Marshal(map[string]string{"name": "Alex"})
	ex, err := pg.InsertExtractExample(ctx, db.InsertExtractExampleParams{
		PromptKind:    "profile_extract",
		InputText:     "My name is Alex",
		GoldOutput:    gold,
		SourceEventID: &ev.ID,
	})
	if err != nil {
		t.Fatalf("InsertExtractExample: %v", err)
	}
	if ex.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if ex.PromptKind != "profile_extract" {
		t.Errorf("PromptKind: got %q", ex.PromptKind)
	}
	if !ex.Active {
		t.Error("new example should be active")
	}
	if ex.SourceEventID == nil || *ex.SourceEventID != ev.ID {
		t.Errorf("SourceEventID: got %v want %d", ex.SourceEventID, ev.ID)
	}
}

func TestExtractExamples_WithoutSourceEvent(t *testing.T) {
	pg, _ := setupFeedbackTest(t)
	ctx := context.Background()

	gold, _ := json.Marshal([]string{"fact1", "fact2"})
	ex, err := pg.InsertExtractExample(ctx, db.InsertExtractExampleParams{
		PromptKind: "fine_extract",
		InputText:  "some text",
		GoldOutput: gold,
	})
	if err != nil {
		t.Fatalf("InsertExtractExample without source: %v", err)
	}
	if ex.SourceEventID != nil {
		t.Error("SourceEventID should be nil when not provided")
	}
}

// --- Migration sanity: tables exist after migration ---

func TestFeedbackEvents_MigrationApplied(t *testing.T) {
	pg, _ := setupFeedbackTest(t)
	ctx := context.Background()

	var exists bool
	err := pg.Pool().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_tables
			WHERE schemaname = 'memos_graph'
			  AND tablename  = 'feedback_events'
		)`).Scan(&exists)
	if err != nil {
		t.Fatalf("probe feedback_events existence: %v", err)
	}
	if !exists {
		t.Error("feedback_events table not found — migration may not have run")
	}

	err = pg.Pool().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_tables
			WHERE schemaname = 'memos_graph'
			  AND tablename  = 'extract_examples'
		)`).Scan(&exists)
	if err != nil {
		t.Fatalf("probe extract_examples existence: %v", err)
	}
	if !exists {
		t.Error("extract_examples table not found — migration may not have run")
	}
}
