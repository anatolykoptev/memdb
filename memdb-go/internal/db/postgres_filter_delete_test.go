package db

// postgres_filter_delete_test.go — unit tests for the helper logic that can
// be exercised without a live Postgres+AGE connection. The happy path of
// DeleteByFilter / DeleteByFileIDs requires a real DB and is covered by the
// integration suite (see ROADMAP-GO-MIGRATION.md T6).

import (
	"context"
	"strings"
	"testing"
)

func TestValidateCubeIDs_Valid(t *testing.T) {
	cases := [][]string{
		{"memos"},
		{"cube_1", "cube-2", "abc123"},
		{"a"},
	}
	for _, c := range cases {
		if err := validateCubeIDs(c); err != nil {
			t.Errorf("validateCubeIDs(%v) unexpected error: %v", c, err)
		}
	}
}

func TestValidateCubeIDs_Invalid(t *testing.T) {
	cases := [][]string{
		{""},
		{"has space"},
		{"semicolon;"},
		{"sqli'--"},
		{strings.Repeat("x", 65)}, // too long
	}
	for _, c := range cases {
		if err := validateCubeIDs(c); err == nil {
			t.Errorf("validateCubeIDs(%v) expected error, got nil", c)
		}
	}
}

func TestBuildUserNameConditions_Empty(t *testing.T) {
	if got := buildUserNameConditions(nil); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
	if got := buildUserNameConditions([]string{}); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestBuildUserNameConditions_Single(t *testing.T) {
	got := buildUserNameConditions([]string{"memos"})
	want := `(ag_catalog.agtype_access_operator(properties::text::agtype, '"user_name"'::agtype) = '"memos"'::agtype)`
	if got != want {
		t.Errorf("mismatch:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestBuildUserNameConditions_Multi(t *testing.T) {
	got := buildUserNameConditions([]string{"a", "b"})
	if !strings.Contains(got, `'"a"'::agtype`) || !strings.Contains(got, `'"b"'::agtype`) {
		t.Errorf("missing cube_id in clause: %s", got)
	}
	if !strings.Contains(got, " OR ") {
		t.Errorf("expected OR join, got: %s", got)
	}
	if got[0] != '(' || got[len(got)-1] != ')' {
		t.Errorf("expected parenthesised clause, got: %s", got)
	}
}

func TestDeleteByFilter_EmptyCubeIDs(t *testing.T) {
	p := NewStubPostgres()
	_, _, err := p.DeleteByFilter(context.Background(), nil, []string{"x = y"})
	if err == nil {
		t.Fatal("expected error for empty cubeIDs")
	}
	if !strings.Contains(err.Error(), "cube_id") {
		t.Errorf("expected cube_id error, got: %v", err)
	}
}

func TestDeleteByFilter_InvalidCubeID(t *testing.T) {
	p := NewStubPostgres()
	_, _, err := p.DeleteByFilter(context.Background(), []string{"bad;id"}, []string{"x = y"})
	if err == nil {
		t.Fatal("expected error for invalid cubeID")
	}
	if !strings.Contains(err.Error(), "invalid cube_id") {
		t.Errorf("expected invalid cube_id error, got: %v", err)
	}
}

func TestDeleteByFilter_EmptyConditions(t *testing.T) {
	p := NewStubPostgres()
	_, _, err := p.DeleteByFilter(context.Background(), []string{"memos"}, nil)
	if err == nil {
		t.Fatal("expected error for empty filter conditions")
	}
	if !strings.Contains(err.Error(), "no filter conditions") {
		t.Errorf("expected missing conditions error, got: %v", err)
	}
}

func TestDeleteByFileIDs_EmptyFileIDs(t *testing.T) {
	p := NewStubPostgres()
	_, _, err := p.DeleteByFileIDs(context.Background(), []string{"memos"}, nil)
	if err == nil {
		t.Fatal("expected error for empty fileIDs")
	}
	if !strings.Contains(err.Error(), "fileIDs is empty") {
		t.Errorf("expected empty fileIDs error, got: %v", err)
	}
}
