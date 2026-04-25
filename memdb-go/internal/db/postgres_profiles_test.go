package db

// postgres_profiles_test.go — unit tests for postgres_profiles.go.
// These tests exercise validation and helper logic without a live database.

import (
	"context"
	"strings"
	"testing"
)

// --- validateProfileKey ---

func TestValidateProfileKey_Valid(t *testing.T) {
	cases := []struct{ userID, cubeID, topic, subTopic string }{
		{"user1", "cube1", "personal", "name"},
		{"u", "c", "t", "s"},
		{"user-x", "tenant-acme", "hobby/sports", "football"},
	}
	for _, c := range cases {
		if err := validateProfileKey(c.userID, c.cubeID, c.topic, c.subTopic); err != nil {
			t.Errorf("validateProfileKey(%q,%q,%q,%q) unexpected error: %v", c.userID, c.cubeID, c.topic, c.subTopic, err)
		}
	}
}

func TestValidateProfileKey_MissingUserID(t *testing.T) {
	err := validateProfileKey("", "cube1", "topic", "sub")
	if err == nil {
		t.Fatal("expected error for empty user_id")
	}
	if !strings.Contains(err.Error(), "user_id") {
		t.Errorf("error should mention user_id, got: %v", err)
	}
}

func TestValidateProfileKey_MissingCubeID(t *testing.T) {
	err := validateProfileKey("user1", "", "topic", "sub")
	if err == nil {
		t.Fatal("expected error for empty cube_id (security audit C1)")
	}
	if !strings.Contains(err.Error(), "cube_id") {
		t.Errorf("error should mention cube_id, got: %v", err)
	}
}

func TestValidateProfileKey_MissingTopic(t *testing.T) {
	err := validateProfileKey("user1", "cube1", "", "sub")
	if err == nil {
		t.Fatal("expected error for empty topic")
	}
	if !strings.Contains(err.Error(), "topic") {
		t.Errorf("error should mention topic, got: %v", err)
	}
}

func TestValidateProfileKey_MissingSubTopic(t *testing.T) {
	err := validateProfileKey("user1", "cube1", "topic", "")
	if err == nil {
		t.Fatal("expected error for empty sub_topic")
	}
	if !strings.Contains(err.Error(), "sub_topic") {
		t.Errorf("error should mention sub_topic, got: %v", err)
	}
}

// --- stub validation paths (no live DB required) ---

func TestInsertProfile_ValidationErrors(t *testing.T) {
	p := NewStubPostgres()
	ctx := context.Background()

	cases := []InsertProfileParams{
		{UserID: "", CubeID: "c", Topic: "t", SubTopic: "s", Memo: "m"},
		{UserID: "u", CubeID: "", Topic: "t", SubTopic: "s", Memo: "m"},
		{UserID: "u", CubeID: "c", Topic: "", SubTopic: "s", Memo: "m"},
		{UserID: "u", CubeID: "c", Topic: "t", SubTopic: "", Memo: "m"},
	}
	for _, params := range cases {
		_, err := p.InsertProfile(ctx, params)
		if err == nil {
			t.Errorf("InsertProfile(%+v) expected validation error, got nil", params)
		}
	}
}

func TestUpdateProfile_ValidationErrors(t *testing.T) {
	p := NewStubPostgres()
	ctx := context.Background()

	cases := []UpdateProfileParams{
		{UserID: "", CubeID: "c", Topic: "t", SubTopic: "s"},
		{UserID: "u", CubeID: "", Topic: "t", SubTopic: "s"},
		{UserID: "u", CubeID: "c", Topic: "", SubTopic: "s"},
		{UserID: "u", CubeID: "c", Topic: "t", SubTopic: ""},
	}
	for _, params := range cases {
		_, err := p.UpdateProfile(ctx, params)
		if err == nil {
			t.Errorf("UpdateProfile(%+v) expected validation error, got nil", params)
		}
	}
}

func TestSoftDeleteProfile_ValidationErrors(t *testing.T) {
	p := NewStubPostgres()
	ctx := context.Background()

	cases := [][4]string{
		{"", "c", "t", "s"},
		{"u", "", "t", "s"},
		{"u", "c", "", "s"},
		{"u", "c", "t", ""},
	}
	for _, c := range cases {
		err := p.SoftDeleteProfile(ctx, c[0], c[1], c[2], c[3])
		if err == nil {
			t.Errorf("SoftDeleteProfile(%v) expected error, got nil", c)
		}
	}
}

func TestGetProfilesByUser_EmptyUserID(t *testing.T) {
	p := NewStubPostgres()
	_, err := p.GetProfilesByUser(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty user_id")
	}
	if !strings.Contains(err.Error(), "user_id") {
		t.Errorf("error should mention user_id, got: %v", err)
	}
}

func TestGetProfilesByUserCube_EmptyArgs(t *testing.T) {
	p := NewStubPostgres()
	ctx := context.Background()

	if _, err := p.GetProfilesByUserCube(ctx, "", "cube1"); err == nil {
		t.Error("expected error for empty user_id")
	}
	if _, err := p.GetProfilesByUserCube(ctx, "user1", ""); err == nil {
		t.Error("expected error for empty cube_id (security audit C1)")
	}
}

func TestGetProfilesByTopic_MissingArgs(t *testing.T) {
	p := NewStubPostgres()
	ctx := context.Background()

	if _, err := p.GetProfilesByTopic(ctx, "", "topic"); err == nil {
		t.Error("expected error for empty user_id")
	}
	if _, err := p.GetProfilesByTopic(ctx, "user1", ""); err == nil {
		t.Error("expected error for empty topic")
	}
}

func TestBulkUpsert_EmptySlice(t *testing.T) {
	p := NewStubPostgres()
	if err := p.BulkUpsert(context.Background(), nil); err != nil {
		t.Errorf("BulkUpsert(nil) should be a no-op, got: %v", err)
	}
	if err := p.BulkUpsert(context.Background(), []InsertProfileParams{}); err != nil {
		t.Errorf("BulkUpsert([]) should be a no-op, got: %v", err)
	}
}

func TestBulkUpsert_ValidationError(t *testing.T) {
	p := NewStubPostgres()
	err := p.BulkUpsert(context.Background(), []InsertProfileParams{
		{UserID: "", CubeID: "c", Topic: "t", SubTopic: "s", Memo: "m"},
	})
	if err == nil {
		t.Fatal("expected validation error for empty user_id")
	}
	if !strings.Contains(err.Error(), "user_id") {
		t.Errorf("error should mention user_id, got: %v", err)
	}
}

// TestBulkUpsert_ValidationError_MissingCubeID guards the security-audit C1
// fix: BulkUpsert must reject rows that are missing the tenant scope before
// they ever reach the database.
func TestBulkUpsert_ValidationError_MissingCubeID(t *testing.T) {
	p := NewStubPostgres()
	err := p.BulkUpsert(context.Background(), []InsertProfileParams{
		{UserID: "u", CubeID: "", Topic: "t", SubTopic: "s", Memo: "m"},
	})
	if err == nil {
		t.Fatal("expected validation error for empty cube_id")
	}
	if !strings.Contains(err.Error(), "cube_id") {
		t.Errorf("error should mention cube_id, got: %v", err)
	}
}

// TestDedupProfileEntries_LastWins verifies that duplicate
// (cube_id, topic, sub_topic) keys within a batch are collapsed to the last
// occurrence (last-wins).
func TestDedupProfileEntries_LastWins(t *testing.T) {
	input := []InsertProfileParams{
		{UserID: "u", CubeID: "cA", Topic: "pref", SubTopic: "color", Memo: "blue"},
		{UserID: "u", CubeID: "cA", Topic: "pref", SubTopic: "size", Memo: "L"},
		{UserID: "u", CubeID: "cA", Topic: "pref", SubTopic: "color", Memo: "red"}, // duplicate → should win
	}
	got := dedupProfileEntries(input)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after dedup, got %d", len(got))
	}
	// color entry should be "red" (last occurrence)
	var colorMemo string
	for _, e := range got {
		if e.SubTopic == "color" {
			colorMemo = e.Memo
		}
	}
	if colorMemo != "red" {
		t.Errorf("last entry should win: got %q want red", colorMemo)
	}
}

// TestDedupProfileEntries_NoDups verifies no-op behaviour when all keys are unique.
func TestDedupProfileEntries_NoDups(t *testing.T) {
	input := []InsertProfileParams{
		{UserID: "u", CubeID: "cA", Topic: "a", SubTopic: "1", Memo: "x"},
		{UserID: "u", CubeID: "cA", Topic: "a", SubTopic: "2", Memo: "y"},
	}
	got := dedupProfileEntries(input)
	if len(got) != 2 {
		t.Fatalf("no-dup slice should be unchanged, got %d entries", len(got))
	}
}

// TestDedupProfileEntries_DifferentCubesDoNotCollide guards the security-audit
// C1 fix: the same (topic, sub_topic) pair landing in two cubes within a
// single batch must NOT be deduplicated — they target different rows.
func TestDedupProfileEntries_DifferentCubesDoNotCollide(t *testing.T) {
	input := []InsertProfileParams{
		{UserID: "u", CubeID: "cA", Topic: "pref", SubTopic: "color", Memo: "blue"},
		{UserID: "u", CubeID: "cB", Topic: "pref", SubTopic: "color", Memo: "red"},
	}
	got := dedupProfileEntries(input)
	if len(got) != 2 {
		t.Fatalf("dedup must keep one row per cube, got %d (input=%+v)", len(got), input)
	}
	cubes := map[string]string{}
	for _, e := range got {
		cubes[e.CubeID] = e.Memo
	}
	if cubes["cA"] != "blue" || cubes["cB"] != "red" {
		t.Errorf("rows survived but with wrong memos: %+v", cubes)
	}
}
