package search

import (
	"context"
	"errors"
	"hash/fnv"
	"testing"
	"unicode"
)

// fakeEmbedder is a deterministic, low-dim stub for unit-testing the
// classifier without standing up embed-server. The only contract it
// honours is that identical inputs produce identical vectors and the
// dimension is stable across calls.
type fakeEmbedder struct {
	dim    int
	vec    func(text string) []float32
	failOn string
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if f.failOn != "" && t == f.failOn {
			return nil, errors.New("embed failure for: " + t)
		}
		out[i] = f.vec(t)
	}
	return out, nil
}

// hashVec maps an input text into a fixed-dim float32 vector via FNV
// hashing. Differs across inputs but is stable per input — perfect for
// "two distinct anchors should produce different centroids" tests.
func hashVec(dim int, salt string, text string) []float32 {
	out := make([]float32, dim)
	h := fnv.New64a()
	h.Write([]byte(salt))
	h.Write([]byte(text))
	seed := h.Sum64()
	for i := 0; i < dim; i++ {
		seed = seed*6364136223846793005 + 1442695040888963407
		// Map to roughly [-1, 1].
		out[i] = float32(int64(seed)>>32) / float32(1<<31)
	}
	return out
}

func TestAnchorsCoverAllCategoriesAndLangs(t *testing.T) {
	for _, cat := range classifierCategoryOrder {
		anchors, ok := anchorQuestions[cat]
		if !ok {
			t.Fatalf("category %s: no anchors registered", cat)
		}
		if len(anchors) < 5 {
			t.Errorf("category %s: expected ≥5 anchors, got %d", cat, len(anchors))
		}
		var en, ru, zh int
		for _, a := range anchors {
			switch anchorLang(a) {
			case "en":
				en++
			case "ru":
				ru++
			case "zh":
				zh++
			}
		}
		if en < 1 {
			t.Errorf("category %s: expected ≥1 EN anchor, got 0", cat)
		}
		if ru < 1 {
			t.Errorf("category %s: expected ≥1 RU anchor, got 0", cat)
		}
		if zh < 1 {
			t.Errorf("category %s: expected ≥1 ZH anchor, got 0", cat)
		}
	}
}

// anchorLang is a coarse classifier sufficient for anchor-coverage tests:
// presence of any Hanzi → zh, any Cyrillic → ru, otherwise en. Real
// language detection is out of scope.
func anchorLang(s string) string {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return "zh"
		}
	}
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return "ru"
		}
	}
	return "en"
}

// pinnedFakeEmbedder maps a short list of "anchor" inputs to fixed unit
// vectors and any other input to a near-copy of one of the anchor vectors
// — used to drive ClassifyTopN into a known top-1 outcome.
type pinnedFakeEmbedder struct {
	dim      int
	exact    map[string][]float32 // text → vector
	fallback []float32            // returned for any unknown text
}

func (p *pinnedFakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := p.exact[t]; ok {
			out[i] = append([]float32(nil), v...)
			continue
		}
		out[i] = append([]float32(nil), p.fallback...)
	}
	return out, nil
}

// basisVec returns a unit vector pointing along one axis of dim-D space.
// Used to give each category a fully-orthogonal centroid, so cosine sim
// to one is 1.0 and to all others is 0.0 — the cleanest possible test
// signal.
func basisVec(dim, axis int) []float32 {
	v := make([]float32, dim)
	v[axis] = 1
	return v
}

func TestClassifyTopN_StubEmbedder(t *testing.T) {
	const dim = 5
	exact := map[string][]float32{}
	axisFor := map[QueryCategory]int{}
	for i, cat := range classifierCategoryOrder {
		axisFor[cat] = i
		for _, a := range anchorQuestions[cat] {
			exact[a] = basisVec(dim, i)
		}
	}
	// Query maps EXACTLY to the single_hop axis vector → top-1 should be
	// single_hop with confidence 1.0.
	exact["how many kids does she have?"] = basisVec(dim, axisFor[QueryCategorySingleHop])
	emb := &pinnedFakeEmbedder{
		dim:      dim,
		exact:    exact,
		fallback: make([]float32, dim), // zero — no signal
	}
	c := newLazyEmbedClassifier(emb)
	top, err := c.ClassifyTopN(context.Background(), "how many kids does she have?", 2)
	if err != nil {
		t.Fatalf("classify error: %v", err)
	}
	if len(top) == 0 {
		t.Fatalf("expected ≥1 result, got 0")
	}
	if top[0].Category != QueryCategorySingleHop {
		t.Errorf("expected top1 single_hop, got %s (conf=%.3f)", top[0].Category, top[0].Confidence)
	}
	if top[0].Confidence < 0.99 {
		t.Errorf("expected top1 confidence ≈1.0, got %.3f", top[0].Confidence)
	}
}

func TestClassifyTopN_AmbiguousQuery(t *testing.T) {
	const dim = 5
	exact := map[string][]float32{}
	axisFor := map[QueryCategory]int{}
	for i, cat := range classifierCategoryOrder {
		axisFor[cat] = i
		for _, a := range anchorQuestions[cat] {
			exact[a] = basisVec(dim, i)
		}
	}
	// Query is the equal-mix of single_hop and temporal axes — both
	// centroids should cosine to ≈ 1/sqrt(2) ≈ 0.707.
	mix := make([]float32, dim)
	mix[axisFor[QueryCategorySingleHop]] = 1
	mix[axisFor[QueryCategoryTemporal]] = 1
	// normalise mix
	mix = normalise(mix)
	exact["ambiguous"] = mix
	emb := &pinnedFakeEmbedder{dim: dim, exact: exact, fallback: make([]float32, dim)}
	c := newLazyEmbedClassifier(emb)
	top, err := c.ClassifyTopN(context.Background(), "ambiguous", 2)
	if err != nil {
		t.Fatalf("classify error: %v", err)
	}
	if len(top) < 2 {
		t.Fatalf("expected 2 entries, got %d", len(top))
	}
	got := map[QueryCategory]float64{
		top[0].Category: top[0].Confidence,
		top[1].Category: top[1].Confidence,
	}
	if _, ok := got[QueryCategorySingleHop]; !ok {
		t.Errorf("expected single_hop in top-2, got %v", got)
	}
	if _, ok := got[QueryCategoryTemporal]; !ok {
		t.Errorf("expected temporal in top-2, got %v", got)
	}
	// Confidences should be close — within 0.05.
	delta := top[0].Confidence - top[1].Confidence
	if delta < 0 {
		delta = -delta
	}
	if delta > 0.05 {
		t.Errorf("expected similar confidences for ambiguous query, got delta=%.3f", delta)
	}
}

func TestEnhanceRetrievalAnswer_NilEmbedderIsBaseOnly(t *testing.T) {
	prompt, hinted, _ := buildAnswerEnhanceSystemPrompt(context.Background(), "what is Carol's job?", nil, "")
	if hinted {
		t.Errorf("expected hinted=false with nil embedder, got true")
	}
	if prompt != loadSkillPrompt() {
		t.Errorf("expected base prompt verbatim with nil embedder, got divergence")
	}
}

func TestEnhanceRetrievalAnswer_DisabledClassifier(t *testing.T) {
	t.Setenv("MEMDB_D10_CLASSIFIER_ENABLED", "false")
	const dim = 5
	exact := map[string][]float32{}
	for i, cat := range classifierCategoryOrder {
		for _, a := range anchorQuestions[cat] {
			exact[a] = basisVec(dim, i)
		}
	}
	exact["whatever"] = basisVec(dim, 0)
	emb := &pinnedFakeEmbedder{dim: dim, exact: exact, fallback: make([]float32, dim)}
	prompt, hinted, _ := buildAnswerEnhanceSystemPrompt(context.Background(), "whatever", emb, "")
	if hinted {
		t.Errorf("expected hinted=false when classifier disabled, got true")
	}
	if prompt != loadSkillPrompt() {
		t.Errorf("expected base prompt verbatim when classifier disabled")
	}
}

func TestClassifyTopN_NilEmbedderNoSignal(t *testing.T) {
	c := newLazyEmbedClassifier(nil)
	top, err := c.ClassifyTopN(context.Background(), "anything", 2)
	if err != nil {
		t.Fatalf("nil embedder should NOT propagate error, got %v", err)
	}
	if len(top) != 1 || top[0].Category != QueryCategoryOpenDomain || top[0].Confidence != 0 {
		t.Errorf("expected single open_domain conf=0 sentinel, got %v", top)
	}
}

func TestClassifyTopN_EmbedErrorReturnsSentinel(t *testing.T) {
	const dim = 4
	emb := &fakeEmbedder{
		dim: dim,
		vec: func(text string) []float32 { return hashVec(dim, "fake", text) },
		// Fail on the FIRST anchor — this short-circuits centroid init,
		// not the per-query call. Both paths must funnel through the same
		// no-signal sentinel.
		failOn: anchorQuestions[QueryCategorySingleHop][0],
	}
	c := newLazyEmbedClassifier(emb)
	top, err := c.ClassifyTopN(context.Background(), "irrelevant", 2)
	// Centroid init failed → first call returns the propagated error AND
	// the no-signal sentinel. Subsequent calls (second test below) reuse
	// the cached error (sync.Once).
	if err == nil {
		t.Errorf("expected centroid-init error to surface on first call")
	}
	if len(top) != 1 || top[0].Category != QueryCategoryOpenDomain {
		t.Errorf("expected no-signal sentinel on init error, got %v", top)
	}
	top2, err2 := c.ClassifyTopN(context.Background(), "another", 2)
	if err2 == nil {
		t.Errorf("expected cached centroid-init error on second call too")
	}
	if len(top2) != 1 || top2[0].Category != QueryCategoryOpenDomain {
		t.Errorf("expected no-signal sentinel on cached init error, got %v", top2)
	}
}
