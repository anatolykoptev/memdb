package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockAtomicLLM serves a fixed JSON response wrapped in OpenAI chat-completion
// envelope. Mirrors mockLLM in extractor_test.go but is duplicated here so the
// test files stay independent.
func mockAtomicLLM(t *testing.T, responseBody string) (*httptest.Server, *AtomicExtractor) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": responseBody}},
			},
		})
	}))
	c := NewClient(srv.URL, "", "gemini-2.0-flash-lite", nil, nil)
	return srv, NewAtomicExtractor(c)
}

// TestExtractAtomicFacts_TenMessageChunkProducesAtLeastFiveFacts is the F8
// acceptance gate: a 10-message LoCoMo chunk must produce ≥5 facts when the
// LLM cooperates. Mocks the LLM with a synthetic 5-fact response that hits
// the 15-80 word window and proper-noun rules.
func TestExtractAtomicFacts_TenMessageChunkProducesAtLeastFiveFacts(t *testing.T) {
	rawLLM := `{"memory": [
		{"id": "0", "text": "User Marcus was promoted to Senior Engineer at Shopify around August 12, 2025 after working toward it for two years and is celebrating with his wife Elena.", "attributed_to": "user"},
		{"id": "1", "text": "Marcus and his wife Elena celebrate special occasions at Osteria Francescana, their go-to restaurant in Toronto, where they had dinner the night of the promotion.", "attributed_to": "user"},
		{"id": "2", "text": "Marcus and his wife Elena are expecting their first baby in March 2026, which Marcus described as exciting and overwhelming during the conversation.", "attributed_to": "user"},
		{"id": "3", "text": "User started taking pottery classes on Tuesday evenings at the Wallace Studio in Toronto and made a ceramic mug with his daughter Sara's face on it.", "attributed_to": "user"},
		{"id": "4", "text": "User's sister Anna recently moved from Vancouver to Portland, Oregon for a new role at Nike, and Marcus mentioned feeling happy but a bit overwhelmed by the back-to-back life changes.", "attributed_to": "user", "linked_memory_ids": ["a1b2c3d4-0000-0000-0000-111111111111"]}
	]}`
	srv, ext := mockAtomicLLM(t, rawLLM)
	defer srv.Close()

	conv := strings.Repeat(
		"user: I have several updates today.\nassistant: Tell me more.\n",
		5)
	candidates := []Candidate{
		{ID: "a1b2c3d4-0000-0000-0000-111111111111", Memory: "User has a sister named Anna"},
	}

	facts, err := ext.ExtractAtomicFacts(context.Background(), conv, candidates, "2025-08-19")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) < 5 {
		t.Fatalf("expected at least 5 atomic facts, got %d", len(facts))
	}
	for i, f := range facts {
		wc := CountWords(f.Text)
		// Mem0 spec: 15-80 words, up to 100 for detail-rich content.
		if wc < AtomicWordMin || wc > AtomicWordMax {
			t.Errorf("fact %d word count %d outside [%d,%d]: %q",
				i, wc, AtomicWordMin, AtomicWordMax, f.Text)
		}
		if !HasProperNoun(f.Text) {
			t.Errorf("fact %d has no proper noun: %q", i, f.Text)
		}
		if f.AttributedTo == "" {
			t.Errorf("fact %d missing attributed_to", i)
		}
	}
	// Spot-check linked_memory_ids was preserved + filtered to valid UUIDs.
	if got := facts[4].LinkedMemoryIDs; len(got) != 1 || got[0] != "a1b2c3d4-0000-0000-0000-111111111111" {
		t.Errorf("expected linked_memory_ids on fact 4, got %#v", got)
	}
}

// TestExtractAtomicFacts_DropsInvalidUUIDs verifies that bogus UUIDs in
// linked_memory_ids are silently filtered, keeping only the valid ones.
func TestExtractAtomicFacts_DropsInvalidUUIDs(t *testing.T) {
	rawLLM := `{"memory": [
		{"id": "0", "text": "User Sara took her dog Poppy to the veterinarian Dr. Lopez at Bay Animal Hospital in San Francisco on March 14, 2025 for an annual checkup.", "attributed_to": "user", "linked_memory_ids": ["not-a-uuid", "a1b2c3d4-0000-0000-0000-111111111111", "also-bogus"]}
	]}`
	srv, ext := mockAtomicLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAtomicFacts(context.Background(), "user: hi", nil, "2025-03-15")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	got := facts[0].LinkedMemoryIDs
	if len(got) != 1 || got[0] != "a1b2c3d4-0000-0000-0000-111111111111" {
		t.Errorf("expected exactly one valid UUID, got %#v", got)
	}
}

// TestExtractAtomicFacts_EmptyOnNoFacts verifies the {"memory": []} contract.
func TestExtractAtomicFacts_EmptyOnNoFacts(t *testing.T) {
	srv, ext := mockAtomicLLM(t, `{"memory": []}`)
	defer srv.Close()

	facts, err := ext.ExtractAtomicFacts(context.Background(), "user: hi", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("expected 0 facts, got %d", len(facts))
	}
}

// TestCountWords + TestHasProperNoun pin the validation helpers used by the
// fact_word_count metric and the proper-noun warning log.
func TestCountWords(t *testing.T) {
	cases := map[string]int{
		"":                                      0,
		"   ":                                   0,
		"one two three":                         3,
		"User Marcus moved to Toronto in 2025.": 7,
	}
	for in, want := range cases {
		if got := CountWords(in); got != want {
			t.Errorf("CountWords(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestHasProperNoun(t *testing.T) {
	yes := []string{
		"User Marcus moved to Toronto",
		"the dog Poppy is happy",
		"Анна переехала в Москву",
	}
	no := []string{
		"i love hiking on weekends",
		"the dog runs fast",
	}
	for _, s := range yes {
		if !HasProperNoun(s) {
			t.Errorf("HasProperNoun(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if HasProperNoun(s) {
			t.Errorf("HasProperNoun(%q) = true, want false", s)
		}
	}
}
