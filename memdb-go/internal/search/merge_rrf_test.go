package search

import (
	"math"
	"os"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// --- TestMergeWithRRF_DefaultDisabled ---

// When MEMDB_USE_GOKIT_RRF is unset, mergeVectorAndFulltextDispatch must
// produce output identical to MergeVectorAndFulltext (bytewise parity).
func TestMergeWithRRF_DefaultDisabled(t *testing.T) {
	os.Unsetenv(goKitRRFEnvVar)

	vec := []db.VectorSearchResult{
		{ID: "a", Properties: `{"id":"a"}`, Score: 0.9},
		{ID: "b", Properties: `{"id":"b"}`, Score: 0.8},
		{ID: "c", Properties: `{"id":"c"}`, Score: 0.7},
	}
	ft := []db.VectorSearchResult{
		{ID: "b", Properties: `{"id":"b"}`, Score: 0.85},
		{ID: "d", Properties: `{"id":"d"}`, Score: 0.6},
	}

	want := MergeVectorAndFulltext(vec, ft)
	got := mergeVectorAndFulltextDispatch(vec, ft)

	if len(want) != len(got) {
		t.Fatalf("parity: len want=%d got=%d", len(want), len(got))
	}
	for i := range want {
		if want[i].ID != got[i].ID {
			t.Errorf("pos %d: want ID=%s got ID=%s", i, want[i].ID, got[i].ID)
		}
		if !approxEqual(want[i].Score, got[i].Score, 1e-12) {
			t.Errorf("pos %d id=%s: want score=%f got=%f", i, want[i].ID, want[i].Score, got[i].Score)
		}
	}
}

// --- TestMergeWithRRF_EnabledFusesThreeSources ---

// When enabled, MergeVectorAndFulltextGokit must apply the Cormack-Clarke
// formula Σ 1/(k+rank_i) correctly.
//
// Hand-computed expectations with k=60 (DefaultRRFK):
//   - "a": rank 1 in vec → 1/(60+1)  = 0.016393
//   - "b": rank 2 in vec + rank 1 in ft → 1/(60+2) + 1/(60+1) = 0.016129 + 0.016393 = 0.032522
//   - "c": rank 3 in vec → 1/(60+3) = 0.015873
//   - "d": rank 2 in ft → 1/(60+2) = 0.016129
//
// Expected order: b > a > d > c
func TestMergeWithRRF_EnabledFusesThreeSources(t *testing.T) {
	os.Setenv(goKitRRFEnvVar, "1")
	os.Unsetenv(rrfKEnvVar)
	t.Cleanup(func() { os.Unsetenv(goKitRRFEnvVar) })

	vec := []db.VectorSearchResult{
		{ID: "a", Properties: `{"id":"a"}`, Score: 0.9},
		{ID: "b", Properties: `{"id":"b"}`, Score: 0.8},
		{ID: "c", Properties: `{"id":"c"}`, Score: 0.7},
	}
	ft := []db.VectorSearchResult{
		{ID: "b", Properties: `{"id":"b"}`, Score: 0.85},
		{ID: "d", Properties: `{"id":"d"}`, Score: 0.6},
	}

	k := 60
	want := map[string]float64{
		"a": 1.0 / float64(k+1),
		"b": 1.0/float64(k+2) + 1.0/float64(k+1),
		"c": 1.0 / float64(k+3),
		"d": 1.0 / float64(k+2),
	}

	got := MergeVectorAndFulltextGokit(vec, ft)

	if len(got) != 4 {
		t.Fatalf("expected 4 results, got %d", len(got))
	}

	scores := make(map[string]float64, len(got))
	for _, r := range got {
		scores[r.ID] = r.Score
	}

	const eps = 1e-9
	for id, wantScore := range want {
		gotScore, ok := scores[id]
		if !ok {
			t.Errorf("missing ID %s in result", id)
			continue
		}
		if math.Abs(gotScore-wantScore) > eps {
			t.Errorf("id=%s score: want %.9f got %.9f", id, wantScore, gotScore)
		}
	}

	// Check ranking order: b > a > d > c
	wantOrder := []string{"b", "a", "d", "c"}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Errorf("rank %d: want %s got %s", i+1, id, got[i].ID)
		}
	}
}

// --- TestMergeWithRRF_EnvKOverride ---

// MEMDB_RRF_K=30 must be used instead of the default 60.
// Spot-check: rank-1 item in vec should have score 1/(30+1) not 1/(60+1).
func TestMergeWithRRF_EnvKOverride(t *testing.T) {
	os.Setenv(goKitRRFEnvVar, "1")
	os.Setenv(rrfKEnvVar, "30")
	t.Cleanup(func() {
		os.Unsetenv(goKitRRFEnvVar)
		os.Unsetenv(rrfKEnvVar)
	})

	vec := []db.VectorSearchResult{
		{ID: "x", Properties: `{"id":"x"}`, Score: 0.9},
		{ID: "y", Properties: `{"id":"y"}`, Score: 0.8},
	}
	ft := []db.VectorSearchResult{
		{ID: "y", Properties: `{"id":"y"}`, Score: 0.75},
	}

	got := mergeVectorAndFulltextDispatch(vec, ft)

	k := 30
	wantX := 1.0 / float64(k+1)                  // x: rank 1 in vec only
	wantY := 1.0/float64(k+2) + 1.0/float64(k+1) // y: rank 2 in vec + rank 1 in ft

	scores := make(map[string]float64, len(got))
	for _, r := range got {
		scores[r.ID] = r.Score
	}

	const eps = 1e-9
	if v, ok := scores["x"]; !ok || math.Abs(v-wantX) > eps {
		t.Errorf("x score: want %.9f got %.9f", wantX, scores["x"])
	}
	if v, ok := scores["y"]; !ok || math.Abs(v-wantY) > eps {
		t.Errorf("y score: want %.9f got %.9f", wantY, scores["y"])
	}

	// y appears in both lists with k=30 and should rank first.
	if len(got) == 0 || got[0].ID != "y" {
		t.Errorf("y should rank first with k=30, got %v", got)
	}
}
