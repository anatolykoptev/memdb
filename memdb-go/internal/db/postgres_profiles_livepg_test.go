//go:build livepg

package db_test

// postgres_profiles_livepg_test.go — end-to-end tests for user_profiles table.
// Requires a live PostgreSQL instance with MemDB migrations applied.
// Run: MEMDB_TEST_POSTGRES_URL=<dsn> go test -tags=livepg ./memdb-go/internal/db/...

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const testProfileUser = "test-profiles-user"

func setupProfilesTest(t *testing.T) (*db.Postgres, func()) {
	t.Helper()
	pg, cleanup0 := setupCubesTest(t) // reuses the existing livepg setup helper
	ctx := context.Background()

	// Remove any leftover rows from previous runs.
	if _, err := pg.Pool().Exec(ctx,
		`DELETE FROM memos_graph.user_profiles WHERE user_id = $1`, testProfileUser,
	); err != nil {
		t.Fatalf("cleanup prior profile rows: %v", err)
	}

	cleanup := func() {
		_, _ = pg.Pool().Exec(context.Background(),
			`DELETE FROM memos_graph.user_profiles WHERE user_id = $1`, testProfileUser)
		cleanup0()
	}
	return pg, cleanup
}

// --- Insert / GetByUser / GetByTopic ---

func TestProfiles_InsertAndGet(t *testing.T) {
	pg, cleanup := setupProfilesTest(t)
	defer cleanup()
	ctx := context.Background()

	entry, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID:   testProfileUser,
		Topic:    "personal",
		SubTopic: "name",
		Memo:     "Alex",
	})
	if err != nil {
		t.Fatalf("InsertProfile: %v", err)
	}
	if entry.ID == 0 {
		t.Error("expected non-zero ID after insert")
	}
	if entry.Confidence != 1.0 {
		t.Errorf("default confidence: got %v want 1.0", entry.Confidence)
	}
	if entry.ExpiredAt != nil {
		t.Error("new row should not be expired")
	}

	profiles, err := pg.GetProfilesByUser(ctx, testProfileUser)
	if err != nil {
		t.Fatalf("GetProfilesByUser: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	if profiles[0].Memo != "Alex" {
		t.Errorf("memo: got %q want Alex", profiles[0].Memo)
	}

	byTopic, err := pg.GetProfilesByTopic(ctx, testProfileUser, "personal")
	if err != nil {
		t.Fatalf("GetProfilesByTopic: %v", err)
	}
	if len(byTopic) != 1 {
		t.Fatalf("expected 1 profile by topic, got %d", len(byTopic))
	}
}

// --- Update ---

func TestProfiles_Update(t *testing.T) {
	pg, cleanup := setupProfilesTest(t)
	defer cleanup()
	ctx := context.Background()

	_, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID: testProfileUser, Topic: "personal", SubTopic: "age", Memo: "30",
	})
	if err != nil {
		t.Fatalf("InsertProfile: %v", err)
	}

	updated, err := pg.UpdateProfile(ctx, db.UpdateProfileParams{
		UserID: testProfileUser, Topic: "personal", SubTopic: "age",
		Memo: "31", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.Memo != "31" {
		t.Errorf("memo after update: got %q want 31", updated.Memo)
	}
	if updated.Confidence != 0.9 {
		t.Errorf("confidence after update: got %v want 0.9", updated.Confidence)
	}
}

func TestProfiles_UpdateNotFound(t *testing.T) {
	pg, cleanup := setupProfilesTest(t)
	defer cleanup()
	ctx := context.Background()

	_, err := pg.UpdateProfile(ctx, db.UpdateProfileParams{
		UserID: testProfileUser, Topic: "nonexistent", SubTopic: "sub", Memo: "x",
	})
	if err == nil {
		t.Fatal("expected ErrProfileNotFound, got nil")
	}
	if err != db.ErrProfileNotFound {
		t.Errorf("expected ErrProfileNotFound, got %v", err)
	}
}

// --- SoftDelete ---

func TestProfiles_SoftDelete(t *testing.T) {
	pg, cleanup := setupProfilesTest(t)
	defer cleanup()
	ctx := context.Background()

	_, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID: testProfileUser, Topic: "work", SubTopic: "role", Memo: "engineer",
	})
	if err != nil {
		t.Fatalf("InsertProfile: %v", err)
	}

	if err := pg.SoftDeleteProfile(ctx, testProfileUser, "work", "role"); err != nil {
		t.Fatalf("SoftDeleteProfile: %v", err)
	}

	// Soft-deleted row must not appear in active queries.
	profiles, err := pg.GetProfilesByUser(ctx, testProfileUser)
	if err != nil {
		t.Fatalf("GetProfilesByUser after delete: %v", err)
	}
	for _, p := range profiles {
		if p.Topic == "work" && p.SubTopic == "role" {
			t.Error("soft-deleted row must not appear in GetProfilesByUser")
		}
	}

	// Soft-deleting an already-deleted row returns ErrProfileNotFound.
	if err := pg.SoftDeleteProfile(ctx, testProfileUser, "work", "role"); err != db.ErrProfileNotFound {
		t.Errorf("second soft-delete: expected ErrProfileNotFound, got %v", err)
	}
}

// --- BulkUpsert ---

func TestProfiles_BulkUpsert_Insert(t *testing.T) {
	pg, cleanup := setupProfilesTest(t)
	defer cleanup()
	ctx := context.Background()

	entries := make([]db.InsertProfileParams, 50)
	for i := range entries {
		entries[i] = db.InsertProfileParams{
			UserID:   testProfileUser,
			Topic:    fmt.Sprintf("topic%02d", i),
			SubTopic: "sub",
			Memo:     fmt.Sprintf("memo %d", i),
		}
	}

	if err := pg.BulkUpsert(ctx, entries); err != nil {
		t.Fatalf("BulkUpsert 50 entries: %v", err)
	}

	profiles, err := pg.GetProfilesByUser(ctx, testProfileUser)
	if err != nil {
		t.Fatalf("GetProfilesByUser: %v", err)
	}
	if len(profiles) != 50 {
		t.Errorf("expected 50 profiles, got %d", len(profiles))
	}
}

func TestProfiles_BulkUpsert_OverwritesMemo(t *testing.T) {
	pg, cleanup := setupProfilesTest(t)
	defer cleanup()
	ctx := context.Background()

	// Insert initial row.
	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{
		{UserID: testProfileUser, Topic: "pref", SubTopic: "language", Memo: "Russian"},
	}); err != nil {
		t.Fatalf("first BulkUpsert: %v", err)
	}

	// Upsert with different memo — should expire old + insert new.
	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{
		{UserID: testProfileUser, Topic: "pref", SubTopic: "language", Memo: "English"},
	}); err != nil {
		t.Fatalf("second BulkUpsert: %v", err)
	}

	profiles, err := pg.GetProfilesByTopic(ctx, testProfileUser, "pref")
	if err != nil {
		t.Fatalf("GetProfilesByTopic: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("expected 1 active profile, got %d", len(profiles))
	}
	if profiles[0].Memo != "English" {
		t.Errorf("memo after overwrite: got %q want English", profiles[0].Memo)
	}
}

func TestProfiles_BulkUpsert_IdempotentSameMemo(t *testing.T) {
	pg, cleanup := setupProfilesTest(t)
	defer cleanup()
	ctx := context.Background()

	params := db.InsertProfileParams{
		UserID: testProfileUser, Topic: "food", SubTopic: "fav", Memo: "pizza",
	}

	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{params}); err != nil {
		t.Fatalf("first BulkUpsert: %v", err)
	}
	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{params}); err != nil {
		t.Fatalf("second BulkUpsert (same memo): %v", err)
	}

	profiles, err := pg.GetProfilesByTopic(ctx, testProfileUser, "food")
	if err != nil {
		t.Fatalf("GetProfilesByTopic: %v", err)
	}
	if len(profiles) != 1 {
		t.Errorf("idempotent upsert must keep exactly 1 active row, got %d", len(profiles))
	}
}

// --- valid_at field ---

func TestProfiles_ValidAt(t *testing.T) {
	pg, cleanup := setupProfilesTest(t)
	defer cleanup()
	ctx := context.Background()

	future := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	entry, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID: testProfileUser, Topic: "event", SubTopic: "meeting", Memo: "board review",
		ValidAt: &future,
	})
	if err != nil {
		t.Fatalf("InsertProfile with valid_at: %v", err)
	}
	if entry.ValidAt == nil {
		t.Fatal("valid_at should be stored")
	}
	if !entry.ValidAt.Equal(future) {
		t.Errorf("valid_at: got %v want %v", entry.ValidAt, future)
	}
}
