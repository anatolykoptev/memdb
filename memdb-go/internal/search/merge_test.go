package search

import (
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// --- PenalizeContradicts tests ---

func TestPenalizeContradicts_Empty(t *testing.T) {
	results := []MergedResult{
		{ID: "a", Score: 0.9},
		{ID: "b", Score: 0.8},
	}
	out := PenalizeContradicts(results, nil)
	if out[0].Score != 0.9 || out[1].Score != 0.8 {
		t.Errorf("empty contradictedIDs should not change scores: %+v", out)
	}
}

func TestPenalizeContradicts_PenaltyApplied(t *testing.T) {
	results := []MergedResult{
		{ID: "winner", Score: 0.9},
		{ID: "loser", Score: 0.85},
		{ID: "neutral", Score: 0.7},
	}
	contradicted := map[string]bool{"loser": true}
	out := PenalizeContradicts(results, contradicted)

	// Find scores by ID
	scores := make(map[string]float64)
	for _, r := range out {
		scores[r.ID] = r.Score
	}

	if scores["winner"] != 0.9 {
		t.Errorf("winner score should be unchanged 0.9, got %f", scores["winner"])
	}
	if scores["neutral"] != 0.7 {
		t.Errorf("neutral score should be unchanged 0.7, got %f", scores["neutral"])
	}
	wantLoser := 0.85 - ContradictsPenalty
	if !approxEqual(scores["loser"], wantLoser, 0.001) {
		t.Errorf("loser score should be %.3f, got %f", wantLoser, scores["loser"])
	}
}

func TestPenalizeContradicts_ScoreFloorZero(t *testing.T) {
	// Score below 0 should be clamped to 0
	results := []MergedResult{
		{ID: "very-low", Score: 0.1},
	}
	contradicted := map[string]bool{"very-low": true}
	out := PenalizeContradicts(results, contradicted)
	if out[0].Score < 0 {
		t.Errorf("score should not go below 0, got %f", out[0].Score)
	}
}

func TestPenalizeContradicts_ResortedByScore(t *testing.T) {
	// After penalty, results should be re-sorted descending
	results := []MergedResult{
		{ID: "a", Score: 0.9},
		{ID: "b", Score: 0.85}, // will be penalized → 0.55
		{ID: "c", Score: 0.7},
	}
	contradicted := map[string]bool{"b": true}
	out := PenalizeContradicts(results, contradicted)

	// Expected order after penalty: a(0.9) > c(0.7) > b(0.55)
	if out[0].ID != "a" {
		t.Errorf("expected a first, got %s", out[0].ID)
	}
	if out[1].ID != "c" {
		t.Errorf("expected c second, got %s", out[1].ID)
	}
	if out[2].ID != "b" {
		t.Errorf("expected b last (penalized), got %s", out[2].ID)
	}
}

func TestPenalizeContradicts_MultipleContradicted(t *testing.T) {
	results := []MergedResult{
		{ID: "a", Score: 0.9},
		{ID: "b", Score: 0.8},
		{ID: "c", Score: 0.75},
		{ID: "d", Score: 0.6},
	}
	contradicted := map[string]bool{"b": true, "c": true}
	out := PenalizeContradicts(results, contradicted)

	scores := make(map[string]float64)
	for _, r := range out {
		scores[r.ID] = r.Score
	}
	if scores["a"] != 0.9 {
		t.Errorf("a unchanged, got %f", scores["a"])
	}
	if !approxEqual(scores["b"], 0.8-ContradictsPenalty, 0.001) {
		t.Errorf("b penalized, got %f", scores["b"])
	}
	if !approxEqual(scores["c"], 0.75-ContradictsPenalty, 0.001) {
		t.Errorf("c penalized, got %f", scores["c"])
	}
	if scores["d"] != 0.6 {
		t.Errorf("d unchanged, got %f", scores["d"])
	}
}

// --- MergeGraphIntoResults tests ---

func TestMergeGraphIntoResults_NewEntry(t *testing.T) {
	existing := []MergedResult{
		{ID: "a", Score: 0.9},
	}
	graph := []db.GraphRecallResult{
		{ID: "b", Properties: `{"id":"b"}`, TagOverlap: 0},
	}
	out := MergeGraphIntoResults(existing, graph)
	if len(out) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out))
	}
	// b should have GraphKeyScore
	var bScore float64
	for _, r := range out {
		if r.ID == "b" {
			bScore = r.Score
		}
	}
	if !approxEqual(bScore, GraphKeyScore, 0.001) {
		t.Errorf("new graph entry should have GraphKeyScore=%.2f, got %f", GraphKeyScore, bScore)
	}
}

func TestMergeGraphIntoResults_ExistingHigherScoreKept(t *testing.T) {
	existing := []MergedResult{
		{ID: "a", Score: 0.95}, // higher than GraphKeyScore
	}
	graph := []db.GraphRecallResult{
		{ID: "a", Properties: `{"id":"a"}`, TagOverlap: 0},
	}
	out := MergeGraphIntoResults(existing, graph)
	if len(out) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out))
	}
	if out[0].Score != 0.95 {
		t.Errorf("higher existing score should be kept, got %f", out[0].Score)
	}
}

func TestMergeGraphIntoResults_TagOverlapBoost(t *testing.T) {
	existing := []MergedResult{}
	graph := []db.GraphRecallResult{
		{ID: "x", Properties: `{"id":"x"}`, TagOverlap: 3},
	}
	out := MergeGraphIntoResults(existing, graph)
	if len(out) != 1 {
		t.Fatalf("expected 1 result, got %d", len(out))
	}
	wantScore := GraphTagBaseScore + GraphTagBonusPerTag*3
	if wantScore > GraphKeyScore {
		wantScore = GraphKeyScore
	}
	if !approxEqual(out[0].Score, wantScore, 0.001) {
		t.Errorf("tag overlap score: want %f, got %f", wantScore, out[0].Score)
	}
}

// --- MergeVectorAndFulltext RRF tests ---

func TestMergeRRF_BothEmpty(t *testing.T) {
	out := MergeVectorAndFulltext(nil, nil)
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

func TestMergeRRF_VectorOnly(t *testing.T) {
	vec := []db.VectorSearchResult{
		{ID: "a", Score: 0.9},
		{ID: "b", Score: 0.8},
	}
	out := MergeVectorAndFulltext(vec, nil)
	if len(out) != 2 {
		t.Fatalf("expected 2, got %d", len(out))
	}
	// a should rank higher than b
	if out[0].ID != "a" {
		t.Errorf("expected a first, got %s", out[0].ID)
	}
}

func TestMergeRRF_SharedIDBoost(t *testing.T) {
	// ID "a" appears in both vector and fulltext → higher RRF score
	vec := []db.VectorSearchResult{
		{ID: "a", Score: 0.9},
		{ID: "b", Score: 0.8},
	}
	ft := []db.VectorSearchResult{
		{ID: "a", Score: 0.7},
		{ID: "c", Score: 0.6},
	}
	out := MergeVectorAndFulltext(vec, ft)
	// "a" appears in both lists → highest RRF score
	if out[0].ID != "a" {
		t.Errorf("shared ID 'a' should rank first due to RRF boost, got %s", out[0].ID)
	}
}
