package search

import "testing"

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
