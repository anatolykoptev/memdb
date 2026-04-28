package llm

import (
	"context"
	"net/http"
	"testing"
)

// TestDecideInvalidation_ShortCircuits_NoCandidates verifies that we never
// touch the LLM when there are no existing facts to judge — saves a round-trip
// on the (common) first-touch path for a fresh subject.
func TestDecideInvalidation_ShortCircuits_NoCandidates(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{}) // zero turns programmed
	defer f.close()

	c := NewSimpleClient(f.server.URL, "", "test-model")
	out, err := DecideInvalidation(context.Background(), c, "lives in Berlin", "Alice", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Invalidate) != 0 || out.Confidence != 0 {
		t.Fatalf("expected zero decision, got %+v", out)
	}
	if f.calls.Load() != 0 {
		t.Fatalf("expected 0 LLM calls on empty candidate list, got %d", f.calls.Load())
	}
}

// TestDecideInvalidation_EmptyInputs covers blank newFact / subject.
func TestDecideInvalidation_EmptyInputs(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{})
	defer f.close()

	c := NewSimpleClient(f.server.URL, "", "test-model")
	cases := []struct {
		name    string
		newFact string
		subject string
	}{
		{"empty newFact", "", "Alice"},
		{"empty subject", "lives in Berlin", ""},
		{"both empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := DecideInvalidation(context.Background(), c, tc.newFact, tc.subject,
				[]FactRef{{ID: "1", Text: "lives in Paris"}})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(out.Invalidate) != 0 {
				t.Fatalf("expected no invalidations, got %v", out.Invalidate)
			}
		})
	}
	if f.calls.Load() != 0 {
		t.Fatalf("expected 0 LLM calls on empty inputs, got %d", f.calls.Load())
	}
}

// TestDecideInvalidation_HappyPath checks a typical supersession reply parses
// and clamps cleanly.
func TestDecideInvalidation_HappyPath(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusOK, content: `{"invalidate":["e1"],"confidence":0.85,"reason":"city changed"}`},
	})
	defer f.close()

	c := NewSimpleClient(f.server.URL, "", "test-model")
	out, err := DecideInvalidation(context.Background(), c,
		"lives in Berlin", "Alice",
		[]FactRef{
			{ID: "e1", Text: "lives in Paris"},
			{ID: "e2", Text: "works at Acme"},
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Invalidate) != 1 || out.Invalidate[0] != "e1" {
		t.Fatalf("expected [e1], got %v", out.Invalidate)
	}
	if out.Confidence < EdgeInvalidationConfidenceThreshold {
		t.Fatalf("expected confidence >= threshold, got %v", out.Confidence)
	}
}

// TestDecideInvalidation_LowConfidence_Returned verifies that low-confidence
// decisions are returned faithfully — the gating happens at the call site,
// not in the judge. The function's contract is "report what the LLM said".
func TestDecideInvalidation_LowConfidence_Returned(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusOK, content: `{"invalidate":["e1"],"confidence":0.4}`},
	})
	defer f.close()

	c := NewSimpleClient(f.server.URL, "", "test-model")
	out, err := DecideInvalidation(context.Background(), c, "x", "Alice",
		[]FactRef{{ID: "e1", Text: "y"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Confidence >= EdgeInvalidationConfidenceThreshold {
		t.Fatalf("expected low confidence, got %v", out.Confidence)
	}
	// The IDs are still echoed back — caller decides whether to act on them.
	if len(out.Invalidate) != 1 {
		t.Fatalf("expected ID echoed back regardless of confidence, got %v", out.Invalidate)
	}
}

// TestDecideInvalidation_HallucinatedIDsDropped guards against the LLM
// returning IDs that aren't in the candidate list (defensive — keeps a
// downstream UPDATE … WHERE id IN (...) from firing on stale rows).
func TestDecideInvalidation_HallucinatedIDsDropped(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusOK, content: `{"invalidate":["e1","does-not-exist"],"confidence":0.9}`},
	})
	defer f.close()

	c := NewSimpleClient(f.server.URL, "", "test-model")
	out, err := DecideInvalidation(context.Background(), c, "x", "Alice",
		[]FactRef{{ID: "e1", Text: "y"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Invalidate) != 1 || out.Invalidate[0] != "e1" {
		t.Fatalf("expected only known IDs, got %v", out.Invalidate)
	}
}

// TestDecideInvalidation_ConfidenceClamped guards against an LLM that returns
// out-of-range confidence values (seen in the wild on small open models).
func TestDecideInvalidation_ConfidenceClamped(t *testing.T) {
	f := newChatStructuredFixture(t, []chatTurn{
		{status: http.StatusOK, content: `{"invalidate":[],"confidence":2.5}`},
		{status: http.StatusOK, content: `{"invalidate":[],"confidence":-0.5}`},
	})
	defer f.close()

	c := NewSimpleClient(f.server.URL, "", "test-model")
	hi, err := DecideInvalidation(context.Background(), c, "x", "s",
		[]FactRef{{ID: "e1", Text: "y"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hi.Confidence != 1.0 {
		t.Fatalf("expected confidence clamped to 1.0, got %v", hi.Confidence)
	}
	lo, err := DecideInvalidation(context.Background(), c, "x", "s",
		[]FactRef{{ID: "e1", Text: "y"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lo.Confidence != 0.0 {
		t.Fatalf("expected confidence clamped to 0.0, got %v", lo.Confidence)
	}
}

// TestEdgeInvalidationThreshold_PinsValue makes the constant load-bearing for
// downstream callers — change it here and the test fails until the doc /
// caller code is updated together.
func TestEdgeInvalidationThreshold_PinsValue(t *testing.T) {
	if EdgeInvalidationConfidenceThreshold != 0.7 {
		t.Fatalf("threshold drifted: got %v, expected 0.7 — coordinate changes with edge invalidation callers", EdgeInvalidationConfidenceThreshold)
	}
}
