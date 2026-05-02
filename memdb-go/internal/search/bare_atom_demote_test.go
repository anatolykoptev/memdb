package search

import (
	"context"
	"fmt"
	"os"
	"testing"
)

// makeResult is a test helper that builds a MergedResult with a memory text in Properties.
func makeResult(id, memory string, score float64) MergedResult {
	return MergedResult{
		ID:         id,
		Properties: fmt.Sprintf(`{"id":%q,"memory":%q}`, id, memory),
		Score:      score,
	}
}

func TestDemoteBareAtoms_DisabledByEnv(t *testing.T) {
	t.Setenv(demoteBareAtomsEnvVar, "0")

	short := makeResult("short", "No", 1.0)
	long := makeResult("long", "Alice has been friends with Bob for seven years now", 0.98)
	results := []MergedResult{short, long}

	got := DemoteBareAtoms(context.Background(), results)
	if got[0].ID != "short" {
		t.Errorf("expected rank-1 to stay 'short' when disabled, got %s", got[0].ID)
	}
}

func TestDemoteBareAtoms_BareAtomAtRank1_Demoted(t *testing.T) {
	os.Unsetenv(demoteBareAtomsEnvVar)
	os.Unsetenv(bareAtomMaxTokensEnvVar)
	os.Unsetenv(bareAtomMinLongEnvVar)
	os.Unsetenv(bareAtomScoreMarginEnvVar)

	// "No" = 1 token (≤2), rank-1 score=1.000
	// Narrative = 10 tokens (≥6), score=0.99 (within 5% of 1.000: gap=0.01/1.0=1%)
	bare := makeResult("bare", "No", 1.000)
	narrative := makeResult("narrative", "Alice has been friends with Bob for seven years now", 0.99)
	results := []MergedResult{bare, narrative}

	got := DemoteBareAtoms(context.Background(), results)
	if got[0].ID != "narrative" {
		t.Errorf("expected 'narrative' at rank-1 after demotion, got %s", got[0].ID)
	}
	if got[1].ID != "bare" {
		t.Errorf("expected 'bare' at rank-2 after demotion, got %s", got[1].ID)
	}
}

func TestDemoteBareAtoms_ScoreGapTooLarge_NoDemotion(t *testing.T) {
	os.Unsetenv(demoteBareAtomsEnvVar)
	os.Unsetenv(bareAtomMaxTokensEnvVar)
	os.Unsetenv(bareAtomMinLongEnvVar)
	os.Unsetenv(bareAtomScoreMarginEnvVar)

	// bare atom at rank-1, but narrative is 20% lower — exceeds default 5% margin
	bare := makeResult("bare", "Yes", 1.000)
	narrative := makeResult("narrative", "Alice has been friends with Bob for seven years since childhood", 0.80)
	results := []MergedResult{bare, narrative}

	got := DemoteBareAtoms(context.Background(), results)
	if got[0].ID != "bare" {
		t.Errorf("expected 'bare' to stay at rank-1 (score gap too large), got %s", got[0].ID)
	}
}

func TestDemoteBareAtoms_Rank1IsLong_NoDemotion(t *testing.T) {
	os.Unsetenv(demoteBareAtomsEnvVar)

	// Rank-1 is already long — demotion guard should not fire.
	long1 := makeResult("long1", "Alice met Bob at a conference in 2019 and they became close friends", 0.99)
	short2 := makeResult("short2", "Yes", 0.98)
	results := []MergedResult{long1, short2}

	got := DemoteBareAtoms(context.Background(), results)
	if got[0].ID != "long1" {
		t.Errorf("expected 'long1' at rank-1 (no demotion needed), got %s", got[0].ID)
	}
}

func TestDemoteBareAtoms_OnlyOneResult_NoDemotion(t *testing.T) {
	os.Unsetenv(demoteBareAtomsEnvVar)

	results := []MergedResult{makeResult("only", "No", 1.0)}
	got := DemoteBareAtoms(context.Background(), results)
	if len(got) != 1 || got[0].ID != "only" {
		t.Errorf("single-item slice should not be modified")
	}
}

func TestDemoteBareAtoms_EmptySlice_NoDemotion(t *testing.T) {
	os.Unsetenv(demoteBareAtomsEnvVar)

	got := DemoteBareAtoms(context.Background(), nil)
	if len(got) != 0 {
		t.Errorf("nil input should return empty slice")
	}
}

func TestDemoteBareAtoms_CustomMaxTokens_FourTokensNotBare(t *testing.T) {
	// With MEMDB_BARE_ATOM_MAX_TOKENS=3, a 4-token doc is NOT a bare atom.
	t.Setenv(bareAtomMaxTokensEnvVar, "3")
	os.Unsetenv(demoteBareAtomsEnvVar)
	os.Unsetenv(bareAtomMinLongEnvVar)
	os.Unsetenv(bareAtomScoreMarginEnvVar)
	t.Cleanup(func() { os.Unsetenv(bareAtomMaxTokensEnvVar) })

	// "I am fine" = 3 tokens (≤3 → bare atom candidate)
	// "I am totally fine here today" = 7 tokens, within 5% margin
	bare4 := makeResult("bare4", "I am fine", 1.0)
	long := makeResult("long", "I am totally fine here today thank you", 0.98)
	results := []MergedResult{bare4, long}

	got := DemoteBareAtoms(context.Background(), results)
	// "I am fine" is exactly 3 tokens which equals MEMDB_BARE_ATOM_MAX_TOKENS=3, so bare → demote
	if got[0].ID != "long" {
		t.Errorf("expected 'long' at rank-1 (3-token doc is bare with max=3), got %s", got[0].ID)
	}
}

func TestDemoteBareAtoms_CustomScoreMargin_DemotesWithWiderMargin(t *testing.T) {
	t.Setenv(bareAtomScoreMarginEnvVar, "0.20") // allow 20% gap
	os.Unsetenv(demoteBareAtomsEnvVar)
	os.Unsetenv(bareAtomMaxTokensEnvVar)
	os.Unsetenv(bareAtomMinLongEnvVar)
	t.Cleanup(func() { os.Unsetenv(bareAtomScoreMarginEnvVar) })

	bare := makeResult("bare", "No", 1.000)
	// Narrative is 15% lower — within 20% margin, so demotion should fire
	narrative := makeResult("narrative", "Alice has been friends with Bob for seven years", 0.85)
	results := []MergedResult{bare, narrative}

	got := DemoteBareAtoms(context.Background(), results)
	if got[0].ID != "narrative" {
		t.Errorf("expected 'narrative' at rank-1 with 20%% margin, got %s", got[0].ID)
	}
}

func TestDemoteBareAtoms_Rank3Candidate_Demoted(t *testing.T) {
	os.Unsetenv(demoteBareAtomsEnvVar)
	os.Unsetenv(bareAtomMaxTokensEnvVar)
	os.Unsetenv(bareAtomMinLongEnvVar)
	os.Unsetenv(bareAtomScoreMarginEnvVar)

	// Rank-1 = bare atom, rank-2 = also short, rank-3 = long narrative within margin
	bare := makeResult("bare", "Yes", 1.000)
	short2 := makeResult("short2", "7 years", 0.99)
	long3 := makeResult("long3", "Alice and Bob have known each other for exactly seven years since college", 0.98)
	results := []MergedResult{bare, short2, long3}

	got := DemoteBareAtoms(context.Background(), results)
	if got[0].ID != "long3" {
		t.Errorf("expected 'long3' at rank-1 (promoted from rank-3), got %s", got[0].ID)
	}
	if got[2].ID != "bare" {
		t.Errorf("expected 'bare' demoted to rank-3, got %s", got[2].ID)
	}
}

func TestWordTokenCount(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"No", 1},
		{"Yes", 1},
		{"7 years", 2},
		{"I am fine", 3},
		{"Alice has been friends with Bob for seven years now", 10},
		{"", 0},
		{"  ", 0},
	}
	for _, tc := range cases {
		got := wordTokenCount(tc.input)
		if got != tc.want {
			t.Errorf("wordTokenCount(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestMemoryText_ParsesMemoryField(t *testing.T) {
	r := makeResult("id1", "Hello world", 0.9)
	if got := memoryText(r); got != "Hello world" {
		t.Errorf("memoryText = %q, want %q", got, "Hello world")
	}
}

func TestMemoryText_EmptyOnBadJSON(t *testing.T) {
	r := MergedResult{ID: "bad", Properties: "{invalid", Score: 1.0}
	if got := memoryText(r); got != "" {
		t.Errorf("memoryText with bad JSON = %q, want empty", got)
	}
}
