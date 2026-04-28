package search

import (
	"math"
	"testing"
)

// TestCat2Heuristic pins the isCat2Query heuristic against a representative
// sample of LoCoMo category-2 (temporal multi-hop) phrasings and a set of
// non-cat-2 counterexamples.
//
// Acceptance criterion (F9): covers ≥ 5 genuine cat-2 phrasings and verifies
// false-positive safety on common single-hop question starters.
func TestCat2Heuristic(t *testing.T) {
	t.Parallel()

	type tc struct {
		query string
		want  bool
	}

	cases := []tc{
		// --- genuine cat-2 phrasings (should match) ---
		{"When did Marcus get promoted?", true},
		{"when did they first meet?", true}, // lower-case
		{"How long have they been dating?", true},
		{"How many months ago did she start the new job?", true},
		{"After what event did Sophia move to Paris?", true},
		{"Before what milestone did he quit smoking?", true},
		{"At what time did the party start?", true},
		{"At what point did their relationship change?", true},
		{"What year did Marcus graduate?", true},
		{"What month did she launch her business?", true},
		{"What date was the wedding?", true},
		{"What day did they break up?", true},

		// --- non-cat-2 / single-hop (should NOT match) ---
		{"What is Marcus's job?", false},
		{"Where does Sophia live?", false},
		{"Who introduced them?", false},
		{"Did they go to the same school?", false},
		{"How many siblings does she have?", false},
		{"Tell me about their hobbies.", false},
		{"Is Marcus married?", false},
		{"What are Sophia's hobbies?", false},
	}

	for _, c := range cases {
		got := isCat2Query(c.query)
		if got != c.want {
			t.Errorf("isCat2Query(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

// TestApplyCat2Threshold_Cat2_LowersRelativity verifies that a cat-2 query
// with a non-zero Relativity above the threshold has its Relativity lowered.
func TestApplyCat2Threshold_Cat2_LowersRelativity(t *testing.T) {
	t.Parallel()
	st := &pipelineState{Params: SearchParams{Query: "When did Marcus get promoted?", Relativity: 0.2}}
	cat := applyCat2Threshold(st)
	if cat != "cat2" {
		t.Errorf("category: got %q want %q", cat, "cat2")
	}
	want := defaultCat2Threshold // 0.05
	if math.Abs(st.Params.Relativity-want) > 1e-9 {
		t.Errorf("Relativity: got %v want %v", st.Params.Relativity, want)
	}
}

// TestApplyCat2Threshold_NonCat2_PreservesRelativity verifies that a non-cat-2
// query leaves Relativity unchanged.
func TestApplyCat2Threshold_NonCat2_PreservesRelativity(t *testing.T) {
	t.Parallel()
	st := &pipelineState{Params: SearchParams{Query: "Tell me about Marcus.", Relativity: 0.2}}
	cat := applyCat2Threshold(st)
	if cat != "other" {
		t.Errorf("category: got %q want %q", cat, "other")
	}
	if math.Abs(st.Params.Relativity-0.2) > 1e-9 {
		t.Errorf("Relativity changed: got %v want 0.2", st.Params.Relativity)
	}
}

// TestApplyCat2Threshold_Cat2_RespectsZeroRelativity verifies that
// Relativity == 0 ("no threshold filter") is never raised to cat2Threshold().
// This was the I1 bug: the old == 0 branch set Relativity = 0.05, turning
// "no filter" into an unwanted filter.
func TestApplyCat2Threshold_Cat2_RespectsZeroRelativity(t *testing.T) {
	t.Parallel()
	st := &pipelineState{Params: SearchParams{Query: "When did Marcus get promoted?", Relativity: 0.0}}
	cat := applyCat2Threshold(st)
	if cat != "cat2" {
		t.Errorf("category: got %q want %q", cat, "cat2")
	}
	if st.Params.Relativity != 0.0 {
		t.Errorf("Relativity should stay 0 (no filter), got %v", st.Params.Relativity)
	}
}

// TestCat2Threshold_EnvOutOfRange_FallsBackToDefault pins the env-fallback
// behavior for cat2Threshold: out-of-range and unparseable values must fall
// back to 0.05, not "clamp" to the nearest bound.
func TestCat2Threshold_EnvOutOfRange_FallsBackToDefault(t *testing.T) {
	cases := []string{"-0.5", "0.99", "foo", "1.5"}
	for _, env := range cases {
		t.Run(env, func(t *testing.T) {
			t.Setenv("MEMDB_CAT2_THRESHOLD", env)
			if got := cat2Threshold(); math.Abs(got-defaultCat2Threshold) > 1e-9 {
				t.Errorf("env=%q want %v (default), got %v", env, defaultCat2Threshold, got)
			}
		})
	}
}
