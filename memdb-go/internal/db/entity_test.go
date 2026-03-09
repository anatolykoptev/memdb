package db

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// --- NormalizeEntityID tests ---

func TestNormalizeEntityID_Basic(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Yandex", "yandex"},
		{"ЯНДЕКС", "яндекс"},
		{"  Ivan  ", "ivan"},
		{"Google LLC", "google llc"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		got := NormalizeEntityID(c.input)
		if got != c.want {
			t.Errorf("NormalizeEntityID(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestNormalizeEntityID_AliasResolution(t *testing.T) {
	// Demonstrates the limitation: "Яндекс" and "Yandex" normalize to different IDs.
	// This is why UpsertEntityNodeWithEmbedding exists — to catch these via cosine similarity.
	id1 := NormalizeEntityID("Яндекс")
	id2 := NormalizeEntityID("Yandex")
	if id1 == id2 {
		t.Errorf("expected different IDs for Яндекс vs Yandex (embedding resolution needed), got same: %q", id1)
	}
}

func TestNormalizeEntityID_SameEntitySameID(t *testing.T) {
	// Same entity with different casing → same normalized ID
	id1 := NormalizeEntityID("Google")
	id2 := NormalizeEntityID("google")
	id3 := NormalizeEntityID("GOOGLE")
	if id1 != id2 || id2 != id3 {
		t.Errorf("expected same ID for Google/google/GOOGLE, got %q %q %q", id1, id2, id3)
	}
}

// --- EntitySimilarityThreshold constant test ---

func TestEntitySimilarityThreshold_Value(t *testing.T) {
	// Threshold should be high enough to avoid false positives (>=0.9)
	// but not so high that it misses obvious aliases (<=0.99)
	if EntitySimilarityThreshold < 0.9 || EntitySimilarityThreshold > 0.99 {
		t.Errorf("EntitySimilarityThreshold=%f should be in [0.9, 0.99]", EntitySimilarityThreshold)
	}
}

// --- EntityRelation struct tests (via llm package types used in graph) ---

// TestEntityEdgeConstants verifies the edge relation constants used in entity graph
func TestEntityEdgeConstants(t *testing.T) {
	// All edge constants must be non-empty strings
	constants := map[string]string{
		"EdgeMergedInto":     EdgeMergedInto,
		"EdgeExtractedFrom":  EdgeExtractedFrom,
		"EdgeContradicts":    EdgeContradicts,
		"EdgeRelated":        EdgeRelated,
		"EdgeMentionsEntity": EdgeMentionsEntity,
	}
	for name, val := range constants {
		if val == "" {
			t.Errorf("edge constant %s is empty", name)
		}
	}
	// Ensure all constants are distinct
	seen := make(map[string]bool)
	for name, val := range constants {
		if seen[val] {
			t.Errorf("duplicate edge constant value %q (from %s)", val, name)
		}
		seen[val] = true
	}
}

// TestEnsureEntityEdgesTableSQL verifies the DDL contains required columns
func TestEnsureEntityEdgesTableSQL(t *testing.T) {
	requiredColumns := []string{
		"from_entity_id",
		"predicate",
		"to_entity_id",
		"memory_id",
		"user_name",
		"valid_at",
		"invalid_at",
		"created_at",
	}
	for _, col := range requiredColumns {
		found := false
		for _, stmt := range []string{EnsureEntityEdgesTableSQL} {
			if len(stmt) > 0 {
				// Simple substring check
				for i := 0; i <= len(stmt)-len(col); i++ {
					if stmt[i:i+len(col)] == col {
						found = true
						break
					}
				}
			}
		}
		if !found {
			t.Errorf("EnsureEntityEdgesTableSQL missing column %q", col)
		}
	}
}

// --- Bi-temporal invalidation SQL tests ---

// TestBiTemporalInvalidation_QueryConstants verifies that recall queries
// filter out invalidated edges (Graphiti bi-temporal pattern).
// These are compile-time checks against the actual query constants.
func TestBiTemporalInvalidation_QueryConstants(t *testing.T) {
	const needle = "invalid_at IS NULL"

	cases := map[string]string{
		"GetMemoriesByEntityIDs": queries.GetMemoriesByEntityIDs,
		"GraphRecallByEdge":      queries.GraphRecallByEdge,
	}
	for name, q := range cases {
		if !strings.Contains(q, needle) {
			t.Errorf("query %s should contain %q for bi-temporal filtering", name, needle)
		}
	}
}

// TestBiTemporalInvalidation_EntityEdgesDDL verifies entity_edges DDL has invalid_at.
func TestBiTemporalInvalidation_EntityEdgesDDL(t *testing.T) {
	const needle = "invalid_at"
	if !containsSubstr(EnsureEntityEdgesTableSQL, needle) {
		t.Errorf("EnsureEntityEdgesTableSQL should contain %q column", needle)
	}
}

func containsSubstr(s, sub string) bool {
	return strings.Contains(s, sub)
}
