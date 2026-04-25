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

// setupProfilesTest returns a Postgres handle and a per-test user ID derived
// from t.Name(). Using t.Name() isolates each test's rows so that concurrent
// runs (CI matrix, -count=2) never interfere with each other.
func setupProfilesTest(t *testing.T) (*db.Postgres, string) {
	t.Helper()
	pg, cleanup0 := setupCubesTest(t) // reuses the existing livepg setup helper
	ctx := context.Background()

	userID := "test-profiles-" + t.Name()

	// Remove any leftover rows from previous runs.
	if _, err := pg.Pool().Exec(ctx,
		`DELETE FROM memos_graph.user_profiles WHERE user_id = $1`, userID,
	); err != nil {
		t.Fatalf("cleanup prior profile rows: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pg.Pool().Exec(context.Background(),
			`DELETE FROM memos_graph.user_profiles WHERE user_id = $1`, userID)
		cleanup0()
	})
	return pg, userID
}

// --- Insert / GetByUser / GetByTopic ---

func TestProfiles_InsertAndGet(t *testing.T) {
	pg, userID := setupProfilesTest(t)
	ctx := context.Background()

	entry, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID:   userID,
		CubeID:   userID,
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

	profiles, err := pg.GetProfilesByUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetProfilesByUser: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	if profiles[0].Memo != "Alex" {
		t.Errorf("memo: got %q want Alex", profiles[0].Memo)
	}

	byTopic, err := pg.GetProfilesByTopic(ctx, userID, "personal")
	if err != nil {
		t.Fatalf("GetProfilesByTopic: %v", err)
	}
	if len(byTopic) != 1 {
		t.Fatalf("expected 1 profile by topic, got %d", len(byTopic))
	}
}

// --- Update ---

func TestProfiles_Update(t *testing.T) {
	pg, userID := setupProfilesTest(t)
	ctx := context.Background()

	_, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID: userID, CubeID: userID, Topic: "personal", SubTopic: "age", Memo: "30",
	})
	if err != nil {
		t.Fatalf("InsertProfile: %v", err)
	}

	updated, err := pg.UpdateProfile(ctx, db.UpdateProfileParams{
		UserID: userID, CubeID: userID, Topic: "personal", SubTopic: "age",
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
	pg, userID := setupProfilesTest(t)
	ctx := context.Background()

	_, err := pg.UpdateProfile(ctx, db.UpdateProfileParams{
		UserID: userID, CubeID: userID, Topic: "nonexistent", SubTopic: "sub", Memo: "x",
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
	pg, userID := setupProfilesTest(t)
	ctx := context.Background()

	_, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID: userID, CubeID: userID, Topic: "work", SubTopic: "role", Memo: "engineer",
	})
	if err != nil {
		t.Fatalf("InsertProfile: %v", err)
	}

	if err := pg.SoftDeleteProfile(ctx, userID, userID, "work", "role"); err != nil {
		t.Fatalf("SoftDeleteProfile: %v", err)
	}

	// Soft-deleted row must not appear in active queries.
	profiles, err := pg.GetProfilesByUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetProfilesByUser after delete: %v", err)
	}
	for _, p := range profiles {
		if p.Topic == "work" && p.SubTopic == "role" {
			t.Error("soft-deleted row must not appear in GetProfilesByUser")
		}
	}

	// Soft-deleting an already-deleted row returns ErrProfileNotFound.
	if err := pg.SoftDeleteProfile(ctx, userID, userID, "work", "role"); err != db.ErrProfileNotFound {
		t.Errorf("second soft-delete: expected ErrProfileNotFound, got %v", err)
	}
}

// --- BulkUpsert ---

func TestProfiles_BulkUpsert_Insert(t *testing.T) {
	pg, userID := setupProfilesTest(t)
	ctx := context.Background()

	entries := make([]db.InsertProfileParams, 50)
	for i := range entries {
		entries[i] = db.InsertProfileParams{
			UserID:   userID,
			CubeID:   userID,
			Topic:    fmt.Sprintf("topic%02d", i),
			SubTopic: "sub",
			Memo:     fmt.Sprintf("memo %d", i),
		}
	}

	if err := pg.BulkUpsert(ctx, entries); err != nil {
		t.Fatalf("BulkUpsert 50 entries: %v", err)
	}

	profiles, err := pg.GetProfilesByUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetProfilesByUser: %v", err)
	}
	if len(profiles) != 50 {
		t.Errorf("expected 50 profiles, got %d", len(profiles))
	}
}

func TestProfiles_BulkUpsert_OverwritesMemo(t *testing.T) {
	pg, userID := setupProfilesTest(t)
	ctx := context.Background()

	// Insert initial row.
	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{
		{UserID: userID, CubeID: userID, Topic: "pref", SubTopic: "language", Memo: "Russian"},
	}); err != nil {
		t.Fatalf("first BulkUpsert: %v", err)
	}

	// Upsert with different memo — should expire old + insert new.
	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{
		{UserID: userID, CubeID: userID, Topic: "pref", SubTopic: "language", Memo: "English"},
	}); err != nil {
		t.Fatalf("second BulkUpsert: %v", err)
	}

	profiles, err := pg.GetProfilesByTopic(ctx, userID, "pref")
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
	pg, userID := setupProfilesTest(t)
	ctx := context.Background()

	params := db.InsertProfileParams{
		UserID: userID, CubeID: userID, Topic: "food", SubTopic: "fav", Memo: "pizza",
	}

	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{params}); err != nil {
		t.Fatalf("first BulkUpsert: %v", err)
	}
	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{params}); err != nil {
		t.Fatalf("second BulkUpsert (same memo): %v", err)
	}

	profiles, err := pg.GetProfilesByTopic(ctx, userID, "food")
	if err != nil {
		t.Fatalf("GetProfilesByTopic: %v", err)
	}
	if len(profiles) != 1 {
		t.Errorf("idempotent upsert must keep exactly 1 active row, got %d", len(profiles))
	}
}

// TestProfiles_BulkUpsert_DuplicateKeysLastWins verifies that when two entries
// with the same (topic, sub_topic) appear in a single batch, the last one wins.
func TestProfiles_BulkUpsert_DuplicateKeysLastWins(t *testing.T) {
	pg, userID := setupProfilesTest(t)
	ctx := context.Background()

	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{
		{UserID: userID, CubeID: userID, Topic: "pref", SubTopic: "color", Memo: "blue"},
		{UserID: userID, CubeID: userID, Topic: "pref", SubTopic: "color", Memo: "red"},
	}); err != nil {
		t.Fatalf("BulkUpsert with duplicate keys: %v", err)
	}

	profiles, err := pg.GetProfilesByTopic(ctx, userID, "pref")
	if err != nil {
		t.Fatalf("GetProfilesByTopic: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("expected 1 active profile after in-batch dedup, got %d", len(profiles))
	}
	if profiles[0].Memo != "red" {
		t.Errorf("last entry should win: got %q want red", profiles[0].Memo)
	}
}

// --- valid_at field ---

func TestProfiles_ValidAt(t *testing.T) {
	pg, userID := setupProfilesTest(t)
	ctx := context.Background()

	future := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	entry, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID: userID, CubeID: userID, Topic: "event", SubTopic: "meeting", Memo: "board review",
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

// --- cube isolation (security audit C1) ---

// setupTwoCubeProfiles creates two cube IDs (A and B) for the same user and
// pre-cleans + post-cleans both. Returns (postgres, userID, cubeA, cubeB).
func setupTwoCubeProfiles(t *testing.T) (*db.Postgres, string, string, string) {
	t.Helper()
	pg, cleanup0 := setupCubesTest(t)
	ctx := context.Background()

	userID := "test-profiles-cube-iso-" + t.Name()
	cubeA := userID + "-cubeA"
	cubeB := userID + "-cubeB"

	// Pre-clean any leftover rows from previous runs of this test.
	if _, err := pg.Pool().Exec(ctx,
		`DELETE FROM memos_graph.user_profiles WHERE user_id = $1`, userID,
	); err != nil {
		t.Fatalf("cleanup prior profile rows: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pg.Pool().Exec(context.Background(),
			`DELETE FROM memos_graph.user_profiles WHERE user_id = $1`, userID)
		cleanup0()
	})
	return pg, userID, cubeA, cubeB
}

// TestProfiles_GetByUserCube_Isolation guards security audit C1: alice's
// profile in cube=A must NEVER show up in a cube=B query (and vice versa).
func TestProfiles_GetByUserCube_Isolation(t *testing.T) {
	pg, userID, cubeA, cubeB := setupTwoCubeProfiles(t)
	ctx := context.Background()

	if _, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID: userID, CubeID: cubeA, Topic: "work", SubTopic: "company", Memo: "Acme Confidential",
	}); err != nil {
		t.Fatalf("InsertProfile cubeA: %v", err)
	}
	if _, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID: userID, CubeID: cubeB, Topic: "work", SubTopic: "company", Memo: "Contoso Public",
	}); err != nil {
		t.Fatalf("InsertProfile cubeB: %v", err)
	}

	rowsA, err := pg.GetProfilesByUserCube(ctx, userID, cubeA)
	if err != nil {
		t.Fatalf("GetProfilesByUserCube(A): %v", err)
	}
	if len(rowsA) != 1 || rowsA[0].Memo != "Acme Confidential" || rowsA[0].CubeID != cubeA {
		t.Errorf("cubeA query returned wrong rows: %+v", rowsA)
	}

	rowsB, err := pg.GetProfilesByUserCube(ctx, userID, cubeB)
	if err != nil {
		t.Fatalf("GetProfilesByUserCube(B): %v", err)
	}
	if len(rowsB) != 1 || rowsB[0].Memo != "Contoso Public" || rowsB[0].CubeID != cubeB {
		t.Errorf("cubeB query returned wrong rows: %+v", rowsB)
	}

	// Cross-tenant probe: a third unrelated cube must return nothing.
	rowsC, err := pg.GetProfilesByUserCube(ctx, userID, "unrelated-cube-"+t.Name())
	if err != nil {
		t.Fatalf("GetProfilesByUserCube(unrelated): %v", err)
	}
	if len(rowsC) != 0 {
		t.Errorf("unrelated cube must yield 0 rows, got %+v", rowsC)
	}
}

// TestProfiles_GetByUserCube_ExcludesNullCubeIDLegacy ensures rows that were
// inserted before migration 0017 (cube_id IS NULL) never leak into a
// cube-scoped chat. The application treats NULL cube_id as "global / pre-cube"
// and the getter must filter them out.
func TestProfiles_GetByUserCube_ExcludesNullCubeIDLegacy(t *testing.T) {
	pg, userID, cubeA, _ := setupTwoCubeProfiles(t)
	ctx := context.Background()

	// Direct SQL: simulate a legacy row by writing cube_id = NULL.
	if _, err := pg.Pool().Exec(ctx, `
INSERT INTO memos_graph.user_profiles
    (user_id, cube_id, topic, sub_topic, memo, confidence, valid_at)
VALUES ($1, NULL, 'work', 'company', 'LEGACY GLOBAL ROW', 1.0, NULL)`,
		userID,
	); err != nil {
		t.Fatalf("insert legacy null-cube row: %v", err)
	}

	// Insert a normal cube-scoped row alongside it.
	if _, err := pg.InsertProfile(ctx, db.InsertProfileParams{
		UserID: userID, CubeID: cubeA, Topic: "personal", SubTopic: "name", Memo: "Alice",
	}); err != nil {
		t.Fatalf("InsertProfile cubeA: %v", err)
	}

	rowsA, err := pg.GetProfilesByUserCube(ctx, userID, cubeA)
	if err != nil {
		t.Fatalf("GetProfilesByUserCube: %v", err)
	}
	for _, r := range rowsA {
		if r.Memo == "LEGACY GLOBAL ROW" {
			t.Fatalf("legacy NULL cube_id row leaked into cube-scoped query: %+v", r)
		}
	}
	if len(rowsA) != 1 || rowsA[0].Memo != "Alice" {
		t.Errorf("cube-scoped query should return only the cube row, got %+v", rowsA)
	}
}

// TestProfiles_BulkUpsert_PerCubeUniqueness confirms the new unique index
// (cube_id, user_id, topic, sub_topic) WHERE expired_at IS NULL lets the same
// (topic, sub_topic) tuple coexist in two cubes for the same user.
func TestProfiles_BulkUpsert_PerCubeUniqueness(t *testing.T) {
	pg, userID, cubeA, cubeB := setupTwoCubeProfiles(t)
	ctx := context.Background()

	if err := pg.BulkUpsert(ctx, []db.InsertProfileParams{
		{UserID: userID, CubeID: cubeA, Topic: "pref", SubTopic: "color", Memo: "blue"},
		{UserID: userID, CubeID: cubeB, Topic: "pref", SubTopic: "color", Memo: "red"},
	}); err != nil {
		t.Fatalf("BulkUpsert across two cubes: %v", err)
	}

	rowsA, err := pg.GetProfilesByUserCube(ctx, userID, cubeA)
	if err != nil || len(rowsA) != 1 || rowsA[0].Memo != "blue" {
		t.Errorf("cubeA: want 1 row memo=blue, got rows=%+v err=%v", rowsA, err)
	}
	rowsB, err := pg.GetProfilesByUserCube(ctx, userID, cubeB)
	if err != nil || len(rowsB) != 1 || rowsB[0].Memo != "red" {
		t.Errorf("cubeB: want 1 row memo=red, got rows=%+v err=%v", rowsB, err)
	}
}
