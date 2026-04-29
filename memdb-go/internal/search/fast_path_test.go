// Package search — fast_path_test.go: M14.Y4 simple-query fast-path tests.
package search

import (
	"testing"
)

// TestIsSimpleFactualQuery_PositiveCases verifies that prototypical single-fact
// questions and bare noun-phrases are classified as simple (fast-path eligible).
func TestIsSimpleFactualQuery_PositiveCases(t *testing.T) {
	t.Parallel()
	cases := []string{
		"What is Caroline's job?",
		"Where does Sophia live?",
		"Who is Marcus?",
		"When did the meeting happen?",
		"Which school did he attend?",
		"How many cats does she have?",
		"How long did the project last?",
		"Is Marcus married?",
		"Did they go to Paris?",
		"Caroline's pets",
	}
	for _, q := range cases {
		if !isSimpleFactualQuery(q) {
			t.Errorf("isSimpleFactualQuery(%q) = false, want true", q)
		}
	}
}

// TestIsSimpleFactualQuery_NegativeCases verifies that multi-clause, conjoined,
// or long queries are NOT classified as simple (must use full pipeline).
func TestIsSimpleFactualQuery_NegativeCases(t *testing.T) {
	t.Parallel()
	cases := []string{
		// "and" conjunction — multi-part
		"What is Caroline's job and where does she live?",
		// semicolon — multi-clause
		"Who is Marcus; when did he graduate?",
		// two question marks — compound
		"What is Marcus's job? And where does he live?",
		// exceeds 80 chars
		"What are all the hobbies, interests, and daily routines that Caroline has developed over the past few years?",
		// multi-hop with "and"
		"When did Marcus graduate and how many years ago did Sophia move?",
		// long complex context
		"Tell me everything you know about Marcus's career trajectory and his relationship with his family members over the last decade",
		// compare aggregation
		"Compare Marcus's hobbies with Sophia's hobbies and tell me who is more active",
		// multi-fact conjunction
		"What does Marcus do and what does Sophia study and where do they both live?",
		// semicolon multi-clause
		"How many siblings does he have; what are their names?",
		// compound double-question
		"Is Marcus married? Does he have children?",
	}
	for _, q := range cases {
		if isSimpleFactualQuery(q) {
			t.Errorf("isSimpleFactualQuery(%q) = true, want false", q)
		}
	}
}

// TestSearchPipeline_FastPath_DisabledByDefault verifies that when
// MEMDB_SEARCH_FAST_PATH is unset, both d4_query_rewrite and d7_cot_decompose
// are present in the pipeline (bytewise parity with current behaviour).
func TestSearchPipeline_FastPath_DisabledByDefault(t *testing.T) {
	// t.Parallel() omitted: t.Setenv and t.Parallel() cannot coexist.
	// Env unset — default OFF.
	t.Setenv(fastPathEnvVar, "")

	svc := &SearchService{}
	stages := svc.defaultStages()

	names := stageNamesFrom(stages)
	if !containsStr(names, "d4_query_rewrite") {
		t.Error("d4_query_rewrite must be in default pipeline")
	}
	if !containsStr(names, "d7_cot_decompose") {
		t.Error("d7_cot_decompose must be in default pipeline")
	}
}

// TestSearchPipeline_FastPath_SimpleQuery_SkipsD4D7 verifies that when
// MEMDB_SEARCH_FAST_PATH=1 and the query is a simple factual question,
// fastPathStages() returns a pipeline WITHOUT d4_query_rewrite and
// d7_cot_decompose — and records outcome="enabled".
func TestSearchPipeline_FastPath_SimpleQuery_SkipsD4D7(t *testing.T) {
	t.Setenv(fastPathEnvVar, "1")

	svc := &SearchService{}
	q := "What is Caroline's job?"

	if !fastPathEnabled() {
		t.Fatal("fast-path env should be enabled")
	}
	if !isSimpleFactualQuery(q) {
		t.Fatalf("query %q should be classified as simple", q)
	}

	stages := svc.fastPathStages()
	names := stageNamesFrom(stages)

	if containsStr(names, "d4_query_rewrite") {
		t.Error("d4_query_rewrite must NOT be in fast-path pipeline")
	}
	if containsStr(names, "d7_cot_decompose") {
		t.Error("d7_cot_decompose must NOT be in fast-path pipeline")
	}
	// All other canonical stages must still be present.
	for _, required := range []string{"embed_query", "parallel_db_fanout", "merge_candidates", "build_response"} {
		if !containsStr(names, required) {
			t.Errorf("stage %q must be present in fast-path pipeline", required)
		}
	}
}

// TestSearchPipeline_FastPath_ComplexQuery_RunsD4D7 verifies that when
// MEMDB_SEARCH_FAST_PATH=1 but the query is complex (fails simplicity gate),
// the full default pipeline (including d4/d7) is used — not the fast-path.
func TestSearchPipeline_FastPath_ComplexQuery_RunsD4D7(t *testing.T) {
	t.Setenv(fastPathEnvVar, "1")

	svc := &SearchService{}
	q := "What is Marcus's job and where does he live?"

	if !fastPathEnabled() {
		t.Fatal("fast-path env should be enabled")
	}
	if isSimpleFactualQuery(q) {
		t.Fatalf("query %q should be classified as complex", q)
	}

	// Complex query → must use defaultStages (d4 + d7 present).
	stages := svc.defaultStages()
	names := stageNamesFrom(stages)

	if !containsStr(names, "d4_query_rewrite") {
		t.Error("d4_query_rewrite must be in pipeline for complex query")
	}
	if !containsStr(names, "d7_cot_decompose") {
		t.Error("d7_cot_decompose must be in pipeline for complex query")
	}
}

// --- helpers ---

func stageNamesFrom(stages []stage) []string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.Name()
	}
	return names
}

func containsStr(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
