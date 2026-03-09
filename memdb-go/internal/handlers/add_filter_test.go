package handlers

import (
	"encoding/json"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

func TestFilterVerdict_ParseJSON(t *testing.T) {
	raw := `{
		"0": {"keep": true, "reason": "Explicitly stated."},
		"1": {"keep": false, "reason": "User denied this."},
		"2": {"keep": true, "reason": "Confirmed by user."}
	}`

	var verdicts map[string]filterVerdict
	if err := json.Unmarshal([]byte(raw), &verdicts); err != nil {
		t.Fatalf("failed to parse verdicts: %v", err)
	}

	if len(verdicts) != 3 {
		t.Fatalf("expected 3 verdicts, got %d", len(verdicts))
	}
	if !verdicts["0"].Keep {
		t.Error("verdict 0: expected keep=true")
	}
	if verdicts["1"].Keep {
		t.Error("verdict 1: expected keep=false")
	}
	if verdicts["1"].Reason != "User denied this." {
		t.Errorf("verdict 1: unexpected reason %q", verdicts["1"].Reason)
	}
	if !verdicts["2"].Keep {
		t.Error("verdict 2: expected keep=true")
	}
}

func TestFilterHallucinatedFacts_NilLLM(t *testing.T) {
	h := &Handler{} // llmChat is nil
	facts := []llm.ExtractedFact{
		{Memory: "User likes Go", Action: llm.MemAdd},
	}
	result := h.filterHallucinatedFacts(t.Context(), "user: I like Go", facts)
	if len(result) != 1 {
		t.Fatalf("expected 1 fact passthrough, got %d", len(result))
	}
	if result[0].Memory != "User likes Go" {
		t.Errorf("expected fact preserved, got %q", result[0].Memory)
	}
}

func TestFilterHallucinatedFacts_EmptyFacts(t *testing.T) {
	h := &Handler{} // llmChat nil, but empty slice should short-circuit first
	result := h.filterHallucinatedFacts(t.Context(), "user: hello", nil)
	if len(result) != 0 {
		t.Fatalf("expected 0 facts, got %d", len(result))
	}
	result = h.filterHallucinatedFacts(t.Context(), "user: hello", []llm.ExtractedFact{})
	if len(result) != 0 {
		t.Fatalf("expected 0 facts, got %d", len(result))
	}
}

func TestFilterVerdict_MissingKey_DefaultsKeep(t *testing.T) {
	// Simulate applying verdicts where a key is missing — fact should be kept.
	verdicts := map[string]filterVerdict{
		"0": {Keep: true, Reason: "ok"},
		// "1" is missing
	}

	facts := []llm.ExtractedFact{
		{Memory: "Fact 0"},
		{Memory: "Fact 1"},
	}

	var kept []llm.ExtractedFact
	for i, f := range facts {
		key := string(rune('0' + i))
		v, ok := verdicts[key]
		if !ok || v.Keep {
			kept = append(kept, f)
		}
	}

	if len(kept) != 2 {
		t.Fatalf("expected 2 kept (missing key = keep), got %d", len(kept))
	}
}
