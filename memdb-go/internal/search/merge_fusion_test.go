package search

import (
	"math"
	"os"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// helpers shared across fusion tests.

func fusionVec() []db.VectorSearchResult {
	return []db.VectorSearchResult{
		{ID: "a", Properties: `{"id":"a"}`, Score: 0.9},
		{ID: "b", Properties: `{"id":"b"}`, Score: 0.8},
		{ID: "c", Properties: `{"id":"c"}`, Score: 0.7},
	}
}

func fusionFT() []db.VectorSearchResult {
	return []db.VectorSearchResult{
		{ID: "b", Properties: `{"id":"b"}`, Score: 0.85},
		{ID: "d", Properties: `{"id":"d"}`, Score: 0.6},
	}
}

func approxEqualFusion(a, b, eps float64) bool {
	return math.Abs(a-b) < eps
}

// --- TestFusionStrategy_DefaultRRF ---

// Env unset → mergeVectorAndFulltextDispatch must call MergeVectorAndFulltext
// (legacy custom merge), identical parity with pre-fusion-PR behaviour.
func TestFusionStrategy_DefaultRRF(t *testing.T) {
	os.Unsetenv(fusionStrategyEnvVar)
	os.Unsetenv(goKitRRFEnvVar)

	vec := fusionVec()
	ft := fusionFT()

	want := MergeVectorAndFulltext(vec, ft)
	got := mergeVectorAndFulltextDispatch(vec, ft)

	if len(want) != len(got) {
		t.Fatalf("parity: len want=%d got=%d", len(want), len(got))
	}
	for i := range want {
		if want[i].ID != got[i].ID {
			t.Errorf("pos %d: want ID=%s got ID=%s", i, want[i].ID, got[i].ID)
		}
	}
}

// --- TestFusionStrategy_WeightedRRF_Applies ---

// strategy=wrrf with MEMDB_RRF_WEIGHTS=semantic:2.0,bm25:1.0,graph:0.5 must
// apply per-source weights. "b" appears in both lists → its weighted-RRF score
// is higher than "a" which appears only in vec.
func TestFusionStrategy_WeightedRRF_Applies(t *testing.T) {
	os.Setenv(fusionStrategyEnvVar, "wrrf")
	os.Setenv(rrfWeightsEnvVar, "semantic:2.0,bm25:1.0,graph:0.5")
	os.Unsetenv(rrfKEnvVar)
	t.Cleanup(func() {
		os.Unsetenv(fusionStrategyEnvVar)
		os.Unsetenv(rrfWeightsEnvVar)
	})

	vec := fusionVec()
	ft := fusionFT()

	got := mergeVectorAndFulltextDispatch(vec, ft)

	if len(got) != 4 {
		t.Fatalf("expected 4 results, got %d", len(got))
	}

	// parseWeightedRRFWeights returns 3 slots; we use [:2] (semantic=2.0, bm25=1.0).
	// With k=60:
	//   "b": rank 2 in vec (w=2.0) + rank 1 in ft (w=1.0)
	//        = 2.0/(60+2) + 1.0/(60+1) = 0.032258 + 0.016393 = 0.048651
	//   "a": rank 1 in vec (w=2.0) = 2.0/(60+1) = 0.032787
	// b must rank above a.
	scoreMap := make(map[string]float64, len(got))
	for _, r := range got {
		scoreMap[r.ID] = r.Score
	}

	k := 60
	wantB := 2.0/float64(k+2) + 1.0/float64(k+1)
	wantA := 2.0 / float64(k+1)
	const eps = 1e-9
	if !approxEqualFusion(scoreMap["b"], wantB, eps) {
		t.Errorf("b score: want %.9f got %.9f", wantB, scoreMap["b"])
	}
	if !approxEqualFusion(scoreMap["a"], wantA, eps) {
		t.Errorf("a score: want %.9f got %.9f", wantA, scoreMap["a"])
	}
	if got[0].ID != "b" {
		t.Errorf("rank 1 should be b, got %s", got[0].ID)
	}
}

// --- TestFusionStrategy_DBSF_Applies ---

// strategy=dbsf must return results for all IDs. DBSF applies z-score
// normalisation per list. We don't verify exact scores (distribution-dependent)
// but do verify:
//   - All IDs from both lists are present in the output.
//   - No NaN or Inf in scores.
func TestFusionStrategy_DBSF_Applies(t *testing.T) {
	os.Setenv(fusionStrategyEnvVar, "dbsf")
	t.Cleanup(func() { os.Unsetenv(fusionStrategyEnvVar) })

	vec := fusionVec()
	ft := fusionFT()

	got := mergeVectorAndFulltextDispatch(vec, ft)

	if len(got) == 0 {
		t.Fatal("DBSF returned empty result")
	}
	idSet := make(map[string]struct{}, len(got))
	for _, r := range got {
		idSet[r.ID] = struct{}{}
		if math.IsNaN(r.Score) || math.IsInf(r.Score, 0) {
			t.Errorf("DBSF produced non-finite score for ID=%s: %f", r.ID, r.Score)
		}
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, ok := idSet[id]; !ok {
			t.Errorf("DBSF missing expected ID=%s", id)
		}
	}
}

// --- TestFusionStrategy_LinearMinMax_Applies ---

// strategy=linear must return normalised scores in [0, Σweights]. With uniform
// weights (env unset → {1.0, 1.0, 1.0}, but we use only first two for 2-list
// linear), scores are in [0, 2.0].
func TestFusionStrategy_LinearMinMax_Applies(t *testing.T) {
	os.Setenv(fusionStrategyEnvVar, "linear")
	os.Unsetenv(rrfWeightsEnvVar) // → uniform {1.0, 1.0} for 2-list
	t.Cleanup(func() { os.Unsetenv(fusionStrategyEnvVar) })

	vec := fusionVec()
	ft := fusionFT()

	got := mergeVectorAndFulltextDispatch(vec, ft)

	if len(got) == 0 {
		t.Fatal("LinearMinMax returned empty result")
	}
	const maxScore = 2.0 + 1e-9 // 1.0 + 1.0 upper bound
	idSet := make(map[string]struct{}, len(got))
	for _, r := range got {
		idSet[r.ID] = struct{}{}
		if r.Score < -1e-9 || r.Score > maxScore {
			t.Errorf("LinearMinMax score out of [0, 2.0] for ID=%s: %f", r.ID, r.Score)
		}
		if math.IsNaN(r.Score) || math.IsInf(r.Score, 0) {
			t.Errorf("LinearMinMax produced non-finite score for ID=%s: %f", r.ID, r.Score)
		}
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, ok := idSet[id]; !ok {
			t.Errorf("LinearMinMax missing expected ID=%s", id)
		}
	}
}

// --- TestFusionStrategy_LegacyBypass ---

// strategy=legacy must call MergeVectorAndFulltext (custom merge) and produce
// bytewise-identical output.
func TestFusionStrategy_LegacyBypass(t *testing.T) {
	os.Setenv(fusionStrategyEnvVar, "legacy")
	t.Cleanup(func() { os.Unsetenv(fusionStrategyEnvVar) })

	vec := fusionVec()
	ft := fusionFT()

	want := MergeVectorAndFulltext(vec, ft)
	got := mergeVectorAndFulltextDispatch(vec, ft)

	if len(want) != len(got) {
		t.Fatalf("legacy parity: len want=%d got=%d", len(want), len(got))
	}
	for i := range want {
		if want[i].ID != got[i].ID {
			t.Errorf("pos %d: want ID=%s got ID=%s", i, want[i].ID, got[i].ID)
		}
		if !approxEqualFusion(want[i].Score, got[i].Score, 1e-12) {
			t.Errorf("pos %d id=%s: want score=%f got=%f", i, want[i].ID, want[i].Score, got[i].Score)
		}
	}
}

// --- TestParseWeightedRRFWeights ---

// Unit tests for parseWeightedRRFWeights.
func TestParseWeightedRRFWeights(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    []float64
		uniform bool
	}{
		{"empty env → uniform", "", nil, true},
		{"valid 3-tuple", "semantic:1.0,bm25:0.7,graph:0.5", []float64{1.0, 0.7, 0.5}, false},
		{"bare values", "1.0,0.7,0.5", []float64{1.0, 0.7, 0.5}, false},
		{"wrong count → uniform", "1.0,0.7", nil, true},
		{"negative weight → uniform", "1.0,-0.5,0.3", nil, true},
		{"bad float → uniform", "1.0,abc,0.3", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				os.Setenv(rrfWeightsEnvVar, tc.env)
			} else {
				os.Unsetenv(rrfWeightsEnvVar)
			}
			t.Cleanup(func() { os.Unsetenv(rrfWeightsEnvVar) })

			got := parseWeightedRRFWeights()
			if len(got) != 3 {
				t.Fatalf("want 3 weights, got %d", len(got))
			}
			if tc.uniform {
				for i, v := range got {
					if v != 1.0 {
						t.Errorf("want uniform [1.0,1.0,1.0] at [%d], got %f", i, v)
					}
				}
				return
			}
			for i, w := range tc.want {
				if !approxEqualFusion(got[i], w, 1e-12) {
					t.Errorf("[%d]: want %f got %f", i, w, got[i])
				}
			}
		})
	}
}
