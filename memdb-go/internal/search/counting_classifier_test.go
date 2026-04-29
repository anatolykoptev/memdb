// Package search — tests for counting_classifier.go (Task #100).
package search

import (
	"testing"
)

// TestIsCountingQuery_PositiveCases verifies that known counting / cardinality
// phrasings are detected by isCountingQuery.
func TestIsCountingQuery_PositiveCases(t *testing.T) {
	t.Parallel()

	cases := []string{
		"How many children does Melanie have?",
		"how many beach trips in 2023?",
		"How many siblings does she have?",
		"How often does Marcus visit his parents?",
		"how often did they go to the cinema?",
		"How much money did he spend last year?",
		"how much time did she spend on homework?",
		"What is the total number of visits?",
		"count of events in July",
		"the number of friends she has",
		"total trips abroad",
		"What is the total amount he owed?",
	}

	for _, q := range cases {
		if !isCountingQuery(q) {
			t.Errorf("isCountingQuery(%q) = false, want true", q)
		}
	}
}

// TestIsCountingQuery_NegativeCases verifies that common non-counting questions
// do NOT trigger the heuristic.
func TestIsCountingQuery_NegativeCases(t *testing.T) {
	t.Parallel()

	cases := []string{
		"What books has Marcus read?",
		"when did they first meet?",
		"Where does Sophia live?",
		"Who introduced them?",
		"Did they go to the same school?",
		"Is Marcus married?",
		"Tell me about their hobbies.",
		"What are Sophia's hobbies?",
		"What year did Marcus graduate?",
		"Why did they break up?",
	}

	for _, q := range cases {
		if isCountingQuery(q) {
			t.Errorf("isCountingQuery(%q) = true, want false", q)
		}
	}
}

// TestApplyCountingBoost_BoostsTopK verifies that a counting query with TopK
// below countingTopK() has its TopK raised to the configured minimum.
func TestApplyCountingBoost_BoostsTopK(t *testing.T) {
	t.Setenv("MEMDB_COUNTING_TOPK", "30")

	st := &pipelineState{Params: SearchParams{
		Query: "How many children does Melanie have?",
		TopK:  10,
	}}
	outcome := applyCountingBoost(st)

	if outcome != "boosted" {
		t.Errorf("outcome = %q, want %q", outcome, "boosted")
	}
	if st.Params.TopK != 30 {
		t.Errorf("TopK = %d, want 30", st.Params.TopK)
	}
}

// TestApplyCountingBoost_AlreadyHighTopK verifies that a counting query with
// TopK already above the boost floor is left unchanged.
func TestApplyCountingBoost_AlreadyHighTopK(t *testing.T) {
	t.Setenv("MEMDB_COUNTING_TOPK", "30")

	st := &pipelineState{Params: SearchParams{
		Query: "How many children does Melanie have?",
		TopK:  50,
	}}
	outcome := applyCountingBoost(st)

	if outcome != "boosted" {
		t.Errorf("outcome = %q, want %q", outcome, "boosted")
	}
	// TopK must NOT be lowered.
	if st.Params.TopK != 50 {
		t.Errorf("TopK = %d, want 50 (must not be lowered)", st.Params.TopK)
	}
}

// TestApplyCountingBoost_NonCountingQuery verifies that a non-counting query
// leaves TopK unchanged and returns "skipped".
func TestApplyCountingBoost_NonCountingQuery(t *testing.T) {
	t.Setenv("MEMDB_COUNTING_TOPK", "30")

	st := &pipelineState{Params: SearchParams{
		Query: "What books has Marcus read?",
		TopK:  10,
	}}
	outcome := applyCountingBoost(st)

	if outcome != "skipped" {
		t.Errorf("outcome = %q, want %q", outcome, "skipped")
	}
	if st.Params.TopK != 10 {
		t.Errorf("TopK = %d, want 10 (must be unchanged)", st.Params.TopK)
	}
}

// TestApplyCountingBoost_DisabledViaEnv verifies that MEMDB_COUNTING_TOPK=0
// disables the boost entirely, leaving TopK unchanged even for counting queries.
func TestApplyCountingBoost_DisabledViaEnv(t *testing.T) {
	t.Setenv("MEMDB_COUNTING_TOPK", "0")

	st := &pipelineState{Params: SearchParams{
		Query: "How many children does Melanie have?",
		TopK:  10,
	}}
	outcome := applyCountingBoost(st)

	if outcome != "skipped" {
		t.Errorf("outcome = %q, want %q", outcome, "skipped")
	}
	if st.Params.TopK != 10 {
		t.Errorf("TopK = %d, want 10 (boost must be disabled)", st.Params.TopK)
	}
}

// TestCountingTopK_Default verifies the default env-unset value is 30.
func TestCountingTopK_Default(t *testing.T) {
	t.Setenv("MEMDB_COUNTING_TOPK", "")

	if got := countingTopK(); got != defaultCountingTopK {
		t.Errorf("countingTopK() = %d, want %d (default)", got, defaultCountingTopK)
	}
}

// TestCountingTopK_CustomValue verifies an in-range env value is respected.
func TestCountingTopK_CustomValue(t *testing.T) {
	t.Setenv("MEMDB_COUNTING_TOPK", "50")

	if got := countingTopK(); got != 50 {
		t.Errorf("countingTopK() = %d, want 50", got)
	}
}

// TestCountingTopK_OutOfRange verifies that values outside [0, 200] fall back
// to the default.
func TestCountingTopK_OutOfRange(t *testing.T) {
	cases := []string{"-1", "201", "foo", "999"}
	for _, env := range cases {
		t.Run(env, func(t *testing.T) {
			t.Setenv("MEMDB_COUNTING_TOPK", env)
			if got := countingTopK(); got != defaultCountingTopK {
				t.Errorf("env=%q: countingTopK() = %d, want default %d", env, got, defaultCountingTopK)
			}
		})
	}
}
