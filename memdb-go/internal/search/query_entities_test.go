package search

// query_entities_test.go — unit tests for ExtractQueryEntities helpers.
//
// Tests:
//  1. extractCapitalizedTokens — basic proper nouns.
//  2. extractCapitalizedTokens — deduplication.
//  3. extractCapitalizedTokens — all-caps acronyms.
//  4. extractCapitalizedTokens — empty string → empty.
//  5. ExtractQueryEntities — empty query → nil.
//  6. ExtractQueryEntities — empty cubeID → nil.
//  7. extractCapitalizedTokens — lowercase-only text → empty.

import (
	"context"
	"testing"
)

// TestExtractCapitalizedTokens_BasicProperNouns checks that capitalized tokens
// (proper names) are extracted correctly.
func TestExtractCapitalizedTokens_BasicProperNouns(t *testing.T) {
	tokens := extractCapitalizedTokens("Tell me about Alice and the OpenAI project")
	got := make(map[string]struct{}, len(tokens))
	for _, tok := range tokens {
		got[tok] = struct{}{}
	}
	for _, want := range []string{"Alice", "OpenAI"} {
		if _, ok := got[want]; !ok {
			t.Errorf("expected token %q in result %v", want, tokens)
		}
	}
}

// TestExtractCapitalizedTokens_Dedup verifies duplicate tokens are returned once.
func TestExtractCapitalizedTokens_Dedup(t *testing.T) {
	tokens := extractCapitalizedTokens("Alice met Alice at Alice's party")
	count := 0
	for _, tok := range tokens {
		if tok == "Alice" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected Alice once, got %d times in %v", count, tokens)
	}
}

// TestExtractCapitalizedTokens_AllCapsAcronyms checks that GPT and API are picked up.
func TestExtractCapitalizedTokens_AllCapsAcronyms(t *testing.T) {
	tokens := extractCapitalizedTokens("what does GPT say about the API")
	got := make(map[string]struct{}, len(tokens))
	for _, tok := range tokens {
		got[tok] = struct{}{}
	}
	for _, want := range []string{"GPT", "API"} {
		if _, ok := got[want]; !ok {
			t.Errorf("expected acronym %q in result %v", want, tokens)
		}
	}
}

// TestExtractCapitalizedTokens_EmptyQuery returns empty slice.
func TestExtractCapitalizedTokens_EmptyQuery(t *testing.T) {
	tokens := extractCapitalizedTokens("")
	if len(tokens) != 0 {
		t.Errorf("expected empty result for empty query, got %v", tokens)
	}
}

// TestExtractCapitalizedTokens_LowercaseOnly verifies no false positives for
// all-lowercase text (e.g. Russian transliteration, plain sentences).
func TestExtractCapitalizedTokens_LowercaseOnly(t *testing.T) {
	tokens := extractCapitalizedTokens("what did she eat for breakfast yesterday")
	if len(tokens) != 0 {
		t.Errorf("expected no tokens for lowercase-only text, got %v", tokens)
	}
}

// TestExtractQueryEntities_EmptyQuery verifies nil return on empty query.
func TestExtractQueryEntities_EmptyQuery(t *testing.T) {
	ids, err := ExtractQueryEntities(context.Background(), nil, "cube-1", "")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if ids != nil {
		t.Errorf("expected nil for empty query, got %v", ids)
	}
}

// TestExtractQueryEntities_EmptyCubeID verifies nil return on empty cubeID.
func TestExtractQueryEntities_EmptyCubeID(t *testing.T) {
	ids, err := ExtractQueryEntities(context.Background(), nil, "", "What did Alice do?")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if ids != nil {
		t.Errorf("expected nil for empty cubeID, got %v", ids)
	}
}

// TestIsAllUpper covers the helper with basic cases.
func TestIsAllUpper(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"API", true},
		{"GPT4", true},
		{"Alice", false},
		{"mixed", false},
		{"UPPER", true},
	}
	for _, c := range cases {
		got := isAllUpper(c.s)
		if got != c.want {
			t.Errorf("isAllUpper(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
