// Package search — tuning_test.go: M4 env-readable hyperparameter tests.
//
// Each accessor: default path, valid override, invalid inputs (non-numeric,
// out-of-bounds) fall back to default.
package search

import (
	"math"
	"testing"
)

func TestTuning_AnswerEnhanceMinRelativity_Default(t *testing.T) {
	if got := answerEnhanceMinRelativity(); got != defaultAnswerEnhanceMinRelativity {
		t.Fatalf("default: got %v want %v", got, defaultAnswerEnhanceMinRelativity)
	}
}

func TestTuning_AnswerEnhanceMinRelativity_EnvOverride(t *testing.T) {
	t.Setenv("MEMDB_D10_MIN_RELATIVITY", "0.3")
	if got := answerEnhanceMinRelativity(); math.Abs(got-0.3) > 1e-9 {
		t.Fatalf("override: got %v want 0.3", got)
	}
}

func TestTuning_AnswerEnhanceMinRelativity_Invalid(t *testing.T) {
	cases := []string{"not-a-number", "-0.1", "1.1", "2.0"}
	for _, v := range cases {
		t.Setenv("MEMDB_D10_MIN_RELATIVITY", v)
		if got := answerEnhanceMinRelativity(); got != defaultAnswerEnhanceMinRelativity {
			t.Errorf("invalid %q: got %v want default %v", v, got, defaultAnswerEnhanceMinRelativity)
		}
	}
}

func TestTuning_StagedShortlistSize(t *testing.T) {
	if got := stagedShortlistSize(); got != defaultStagedShortlistSize {
		t.Fatalf("default: got %d want %d", got, defaultStagedShortlistSize)
	}
	t.Setenv("MEMDB_D5_SHORTLIST_SIZE", "25")
	if got := stagedShortlistSize(); got != 25 {
		t.Fatalf("override: got %d want 25", got)
	}
	// Out of bounds and non-numeric fall back.
	for _, v := range []string{"0", "101", "abc", "-5"} {
		t.Setenv("MEMDB_D5_SHORTLIST_SIZE", v)
		if got := stagedShortlistSize(); got != defaultStagedShortlistSize {
			t.Errorf("invalid %q: got %d want %d", v, got, defaultStagedShortlistSize)
		}
	}
}

func TestTuning_StagedMaxInputSize(t *testing.T) {
	if got := stagedMaxInputSize(); got != defaultStagedMaxInputSize {
		t.Fatalf("default: got %d want %d", got, defaultStagedMaxInputSize)
	}
	t.Setenv("MEMDB_D5_MAX_INPUT_SIZE", "80")
	if got := stagedMaxInputSize(); got != 80 {
		t.Fatalf("override: got %d want 80", got)
	}
	for _, v := range []string{"0", "501", "nope"} {
		t.Setenv("MEMDB_D5_MAX_INPUT_SIZE", v)
		if got := stagedMaxInputSize(); got != defaultStagedMaxInputSize {
			t.Errorf("invalid %q: got %d want %d", v, got, defaultStagedMaxInputSize)
		}
	}
}

func TestTuning_MultihopMaxDepth(t *testing.T) {
	if got := multihopMaxDepth(); got != defaultMultihopMaxDepth {
		t.Fatalf("default: got %d want %d", got, defaultMultihopMaxDepth)
	}
	t.Setenv("MEMDB_D2_MAX_HOP", "3")
	if got := multihopMaxDepth(); got != 3 {
		t.Fatalf("override: got %d want 3", got)
	}
	// Bounds: [1, 5]
	for _, v := range []string{"0", "6", "abc"} {
		t.Setenv("MEMDB_D2_MAX_HOP", v)
		if got := multihopMaxDepth(); got != defaultMultihopMaxDepth {
			t.Errorf("invalid %q: got %d want %d", v, got, defaultMultihopMaxDepth)
		}
	}
}

func TestTuning_MultihopDecay(t *testing.T) {
	if got := multihopDecay(); math.Abs(got-defaultMultihopDecay) > 1e-9 {
		t.Fatalf("default: got %v want %v", got, defaultMultihopDecay)
	}
	t.Setenv("MEMDB_D2_HOP_DECAY", "0.5")
	if got := multihopDecay(); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("override: got %v want 0.5", got)
	}
	for _, v := range []string{"-0.1", "1.5", "abc"} {
		t.Setenv("MEMDB_D2_HOP_DECAY", v)
		if got := multihopDecay(); math.Abs(got-defaultMultihopDecay) > 1e-9 {
			t.Errorf("invalid %q: got %v want %v", v, got, defaultMultihopDecay)
		}
	}
}

func TestTuning_D1BoostSemantic(t *testing.T) {
	if got := d1BoostSemantic(); math.Abs(got-defaultD1BoostSemantic) > 1e-9 {
		t.Fatalf("default: got %v want %v", got, defaultD1BoostSemantic)
	}
	t.Setenv("MEMDB_D1_BOOST_SEMANTIC", "1.25")
	if got := d1BoostSemantic(); math.Abs(got-1.25) > 1e-9 {
		t.Fatalf("override: got %v want 1.25", got)
	}
	for _, v := range []string{"0.9", "2.5", "nope"} {
		t.Setenv("MEMDB_D1_BOOST_SEMANTIC", v)
		if got := d1BoostSemantic(); math.Abs(got-defaultD1BoostSemantic) > 1e-9 {
			t.Errorf("invalid %q: got %v want %v", v, got, defaultD1BoostSemantic)
		}
	}
}

func TestTuning_D1BoostEpisodic(t *testing.T) {
	if got := d1BoostEpisodic(); math.Abs(got-defaultD1BoostEpisodic) > 1e-9 {
		t.Fatalf("default: got %v want %v", got, defaultD1BoostEpisodic)
	}
	t.Setenv("MEMDB_D1_BOOST_EPISODIC", "1.12")
	if got := d1BoostEpisodic(); math.Abs(got-1.12) > 1e-9 {
		t.Fatalf("override: got %v want 1.12", got)
	}
}

func TestTuning_D1HalfLifeDays(t *testing.T) {
	if got := d1HalfLifeDays(); got != defaultD1HalfLifeDays {
		t.Fatalf("default: got %d want %d", got, defaultD1HalfLifeDays)
	}
	t.Setenv("MEMDB_D1_HALF_LIFE_DAYS", "90")
	if got := d1HalfLifeDays(); got != 90 {
		t.Fatalf("override: got %d want 90", got)
	}
	for _, v := range []string{"0", "3651", "abc"} {
		t.Setenv("MEMDB_D1_HALF_LIFE_DAYS", v)
		if got := d1HalfLifeDays(); got != defaultD1HalfLifeDays {
			t.Errorf("invalid %q: got %d want %d", v, got, defaultD1HalfLifeDays)
		}
	}
}

func TestTuning_D1DecayAlpha_ComputedFromHalfLife(t *testing.T) {
	// Default: alpha = ln(2)/180 ≈ 0.003850817...
	got := d1DecayAlpha()
	want := math.Ln2 / float64(defaultD1HalfLifeDays)
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("default alpha: got %v want %v", got, want)
	}
	t.Setenv("MEMDB_D1_HALF_LIFE_DAYS", "60")
	got = d1DecayAlpha()
	want = math.Ln2 / 60.0
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("alpha after half-life=60: got %v want %v", got, want)
	}
}

func TestTuning_HierarchyBoostRespectsEnv(t *testing.T) {
	// Verify the rerank.go hierarchyBoost honours the env accessors.
	t.Setenv("MEMDB_D1_BOOST_SEMANTIC", "1.30")
	t.Setenv("MEMDB_D1_BOOST_EPISODIC", "1.10")
	if got := hierarchyBoost(map[string]any{"hierarchy_level": "semantic"}); math.Abs(got-1.30) > 1e-9 {
		t.Errorf("semantic boost: got %v want 1.30", got)
	}
	if got := hierarchyBoost(map[string]any{"hierarchy_level": "episodic"}); math.Abs(got-1.10) > 1e-9 {
		t.Errorf("episodic boost: got %v want 1.10", got)
	}
	// Raw / unknown unaffected by boost envs.
	if got := hierarchyBoost(map[string]any{"hierarchy_level": "raw"}); got != 1.0 {
		t.Errorf("raw boost: got %v want 1.0", got)
	}
}
