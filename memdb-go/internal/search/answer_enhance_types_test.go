package search

import (
	"strings"
	"testing"
)

func TestClassifyQueryCategory(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  QueryCategory
	}{
		// Rule 1: single_hop — counting / frequency / quantity prefixes.
		{"how_many_basic", "how many siblings does Alice have?", CategorySingleHop},
		{"how_often", "how often did Bob travel last year?", CategorySingleHop},
		{"how_much", "how much did the project cost?", CategorySingleHop},
		{"how_many_with_capitals", "How Many books did Alice and Bob write?", CategorySingleHop},

		// Rule 1 priority over rule 3: "how many ... not" still single_hop.
		{"how_many_outranks_negation", "how many days did Alice not stay?", CategorySingleHop},

		// Rule 2: temporal.
		{"when", "when did Alice meet Bob?", CategoryTemporal},
		{"what_date", "what date was the wedding?", CategoryTemporal},
		{"what_year", "what year did Alice graduate?", CategoryTemporal},
		{"how_long", "how long was the trip?", CategoryTemporal},
		{"how_long_leading_ws", "   how long did Alice live in Paris?", CategoryTemporal},

		// Rule 3: adversarial.
		{"did_not", "did Alice not call Bob?", CategoryAdversarial},
		{"did_never", "did Alice never visit Paris?", CategoryAdversarial},
		{"is_it_true", "is it true that Bob moved to Berlin?", CategoryAdversarial},
		{"is_x_false", "is the claim about Alice false?", CategoryAdversarial},

		// Rule 4: multi_hop.
		{"what_two_caps", "what did Alice tell Bob?", CategoryMultiHop},
		{"what_three_caps", "what did Alice say to Bob about Carol?", CategoryMultiHop},
		{"who_and", "who met Alice and Bob at the party?", CategoryMultiHop},
		{"who_with", "who travelled with Alice last summer?", CategoryMultiHop},
		{"who_while", "who called while Alice was away?", CategoryMultiHop},

		// Rule 5: open_domain catch-all.
		{"open_what_one_cap", "what is the meaning of life?", CategoryOpenDomain},
		{"open_what_no_caps", "what should i do today?", CategoryOpenDomain},
		{"open_who_alone", "who knows the answer", CategoryOpenDomain},
		{"open_arbitrary", "tell me about quantum mechanics", CategoryOpenDomain},

		// Edge cases.
		{"empty", "", CategoryOpenDomain},
		{"whitespace", "   \t\n  ", CategoryOpenDomain},
		{"mixed_case_when", "WHEN did Alice call?", CategoryTemporal},
		{"leading_ws_how_many", "  how many cats?", CategorySingleHop},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := classifyQueryCategory(tc.query)
			if got != tc.want {
				t.Errorf("classifyQueryCategory(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

func TestSelectAnswerEnhancePrompt_AllCategoriesNonEmpty(t *testing.T) {
	seen := make(map[string]QueryCategory, len(AllQueryCategories))
	for _, cat := range AllQueryCategories {
		p := selectAnswerEnhancePrompt(cat)
		if strings.TrimSpace(p) == "" {
			t.Errorf("selectAnswerEnhancePrompt(%q) returned empty", cat)
			continue
		}
		// All prompts must keep the JSON contract + hallucination guard.
		if !strings.Contains(p, "Return strict JSON") {
			t.Errorf("%q prompt missing JSON contract", cat)
		}
		if !strings.Contains(p, "hallucinate") {
			t.Errorf("%q prompt missing hallucination guard", cat)
		}
		if other, dup := seen[p]; dup {
			t.Errorf("%q and %q return the same prompt", cat, other)
		}
		seen[p] = cat
	}
	if len(seen) != len(AllQueryCategories) {
		t.Errorf("expected %d distinct prompts, got %d", len(AllQueryCategories), len(seen))
	}
}

func TestSelectAnswerEnhancePrompt_FallbackOpenDomain(t *testing.T) {
	open := selectAnswerEnhancePrompt(CategoryOpenDomain)

	// Empty category falls back to open_domain.
	if got := selectAnswerEnhancePrompt(""); got != open {
		t.Errorf("empty category: expected open_domain prompt, got different prompt")
	}
	// Unknown category falls back to open_domain.
	if got := selectAnswerEnhancePrompt(QueryCategory("not_a_real_category")); got != open {
		t.Errorf("unknown category: expected open_domain prompt, got different prompt")
	}
}
