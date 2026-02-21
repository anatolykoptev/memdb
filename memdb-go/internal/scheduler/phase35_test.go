package scheduler

// phase35_test.go — unit tests for Phase 3.5 scheduler features:
//   3.5.5 consolidationResult ContradictedIDs JSON parsing
//   3.5.3 DecayAndArchive constants sanity

import (
	"encoding/json"
	"testing"
)

// --- 3.5.5: consolidationResult ContradictedIDs ---

func TestConsolidationResult_ParseContradictedIDs(t *testing.T) {
	raw := `{
		"keep_id":          "aaa",
		"remove_ids":       ["bbb"],
		"contradicted_ids": ["ccc","ddd"],
		"merged_text":      "The user lives in Berlin."
	}`
	var res consolidationResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.KeepID != "aaa" {
		t.Errorf("keep_id: got %q", res.KeepID)
	}
	if len(res.RemoveIDs) != 1 || res.RemoveIDs[0] != "bbb" {
		t.Errorf("remove_ids: got %v", res.RemoveIDs)
	}
	if len(res.ContradictedIDs) != 2 || res.ContradictedIDs[0] != "ccc" || res.ContradictedIDs[1] != "ddd" {
		t.Errorf("contradicted_ids: got %v", res.ContradictedIDs)
	}
	if res.MergedText != "The user lives in Berlin." {
		t.Errorf("merged_text: got %q", res.MergedText)
	}
}

func TestConsolidationResult_NoContradictions(t *testing.T) {
	raw := `{"keep_id":"a","remove_ids":["b"],"merged_text":"fact"}`
	var res consolidationResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res.ContradictedIDs) != 0 {
		t.Errorf("expected empty ContradictedIDs when field absent, got %v", res.ContradictedIDs)
	}
}

// --- 3.5.3: importance decay constants sanity ---

func TestImportanceDecayConstants(t *testing.T) {
	if importanceDecayFactor <= 0 || importanceDecayFactor >= 1 {
		t.Errorf("importanceDecayFactor must be in (0,1), got %v", importanceDecayFactor)
	}
	if importanceArchiveThreshold <= 0 || importanceArchiveThreshold >= 1 {
		t.Errorf("importanceArchiveThreshold must be in (0,1), got %v", importanceArchiveThreshold)
	}
	// A memory at 1.0 must actually reach the archive threshold after enough cycles.
	score := 1.0
	cycles := 0
	for score > importanceArchiveThreshold && cycles < 10000 {
		score *= importanceDecayFactor
		cycles++
	}
	if cycles >= 10000 {
		t.Errorf("importanceDecayFactor=%v never decays below threshold=%v",
			importanceDecayFactor, importanceArchiveThreshold)
	}
	t.Logf("importance 1.0 reaches archive threshold after %d decay cycles (~%.0fh)",
		cycles, float64(cycles)*6)
}
