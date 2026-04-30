package search

import (
	"context"
	"math"
	"strings"
	"testing"
)

// classifierStubDist is a tiny fake that pretends classifyAndDistribute by
// pre-computed centroids. It bypasses the embedder layer so tests can fix
// the cosine-similarities deterministically and assert the softmax math
// downstream.
//
// We do not unit-test classifyAndDistribute through ClassifyTopN here —
// the underlying classifier is already covered in
// answer_enhance_classifier_test.go. This file targets the new pieces:
// percentRound, distributionBlock, and the buildAnswerEnhanceSystemPrompt
// hybrid routing.

func TestPercentRound_Boundaries(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{-0.5, 0},
		{0, 0},
		{0.004, 0},   // rounds down
		{0.005, 1},   // half-up
		{0.499, 50},  // 49.9 rounds to 50
		{0.5, 50},
		{0.999, 100}, // 99.9 rounds to 100
		{1, 100},
		{1.5, 100}, // saturates
	}
	for _, c := range cases {
		got := percentRound(c.in)
		if got != c.want {
			t.Errorf("percentRound(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDistributionBlock_Empty(t *testing.T) {
	if got := distributionBlock(nil, 5); got != "" {
		t.Errorf("nil dist: want empty, got %q", got)
	}
	noSignal := []CategoryConfidence{{Category: QueryCategoryOpenDomain, Confidence: 0}}
	if got := distributionBlock(noSignal, 5); got != "" {
		t.Errorf("no-signal dist: want empty, got %q", got)
	}
}

func TestDistributionBlock_FormatsAllCategories(t *testing.T) {
	dist := []CategoryConfidence{
		{Category: QueryCategoryTemporal, Confidence: 0.62},
		{Category: QueryCategorySingleHop, Confidence: 0.28},
		{Category: QueryCategoryMultiHop, Confidence: 0.07},
		{Category: QueryCategoryOpenDomain, Confidence: 0.02},
		{Category: QueryCategoryAdversarial, Confidence: 0.01},
	}
	got := distributionBlock(dist, 5)
	if got == "" {
		t.Fatal("expected non-empty block")
	}
	wantSubs := []string{
		"Likely question type",
		"temporal 62%",
		"single_hop 28%",
		// long-tail entries (<10%) are trimmed once at least 2 entries
		// are present; multi_hop / open_domain / adversarial drop out
		// of the formatted block.
		"Pick the fitting answer shape",
	}
	for _, w := range wantSubs {
		if !strings.Contains(got, w) {
			t.Errorf("block missing %q.\nFull block:\n%s", w, got)
		}
	}
}

func TestDistributionBlock_TopNTrim(t *testing.T) {
	dist := []CategoryConfidence{
		{Category: QueryCategoryTemporal, Confidence: 0.7},
		{Category: QueryCategorySingleHop, Confidence: 0.2},
		{Category: QueryCategoryMultiHop, Confidence: 0.1},
		{Category: QueryCategoryOpenDomain, Confidence: 0},
		{Category: QueryCategoryAdversarial, Confidence: 0},
	}
	got := distributionBlock(dist, 2)
	// top-2: temporal + single_hop. multi_hop must NOT appear.
	if !strings.Contains(got, "temporal 70%") {
		t.Errorf("missing temporal 70%%, block:\n%s", got)
	}
	if !strings.Contains(got, "single_hop 20%") {
		t.Errorf("missing single_hop 20%%, block:\n%s", got)
	}
	if strings.Contains(got, "multi_hop") {
		t.Errorf("topN=2 should hide multi_hop, block:\n%s", got)
	}
}

func TestClassifyAndDistribute_NoEmbedder(t *testing.T) {
	c := newLazyEmbedClassifier(nil)
	dist, err := c.classifyAndDistribute(context.Background(), "any query")
	if err != nil {
		t.Fatalf("nil embedder should not error, got %v", err)
	}
	if len(dist) != 1 || dist[0].Confidence != 0 {
		t.Errorf("expected no-signal sentinel, got %+v", dist)
	}
}

func TestClassifyAndDistribute_SoftmaxNormalises(t *testing.T) {
	// Use the existing fakeEmbedder + hashVec to produce non-trivial centroids
	// (deterministic, dimension-stable). The exact softmax values aren't
	// meaningful with hashed vectors — we only assert the invariants:
	//   1. Returned slice has 5 entries (one per category).
	//   2. Probabilities sum to 1 ± 1e-9.
	//   3. Sorted desc by confidence.
	emb := &fakeEmbedder{
		dim: 16,
		vec: func(text string) []float32 { return hashVec(16, "dist-test", text) },
	}
	c := newLazyEmbedClassifier(emb)
	dist, err := c.classifyAndDistribute(context.Background(), "How many kids does Alice have?")
	if err != nil {
		t.Fatalf("classifyAndDistribute: %v", err)
	}
	if len(dist) != len(classifierCategoryOrder) {
		t.Fatalf("expected %d entries, got %d", len(classifierCategoryOrder), len(dist))
	}
	var sum float64
	for _, cc := range dist {
		sum += cc.Confidence
	}
	if math.Abs(sum-1.0) > 1e-9 {
		t.Errorf("softmax should sum to 1, got %v", sum)
	}
	for i := 1; i < len(dist); i++ {
		if dist[i].Confidence > dist[i-1].Confidence {
			t.Errorf("not sorted desc: dist[%d]=%v > dist[%d]=%v",
				i, dist[i].Confidence, i-1, dist[i-1].Confidence)
		}
	}
}

func TestBuildAnswerEnhanceSystemPrompt_NilEmbedder(t *testing.T) {
	t.Setenv("MEMDB_D10_CLASSIFIER_ENABLED", "true")
	got, hinted, trace := buildAnswerEnhanceSystemPrompt(context.Background(), "test", nil)
	if got != answerEnhanceSystemPrompt {
		t.Errorf("nil embedder: expected base prompt unchanged")
	}
	if hinted {
		t.Errorf("nil embedder: hinted should be false")
	}
	if trace.Mode != D10RouteBase {
		t.Errorf("nil embedder: expected mode=base, got %s", trace.Mode)
	}
}

func TestBuildAnswerEnhanceSystemPrompt_ClassifierDisabled(t *testing.T) {
	t.Setenv("MEMDB_D10_CLASSIFIER_ENABLED", "false")
	emb := &fakeEmbedder{dim: 8, vec: func(s string) []float32 { return hashVec(8, "x", s) }}
	got, hinted, trace := buildAnswerEnhanceSystemPrompt(context.Background(), "test", emb)
	if got != answerEnhanceSystemPrompt {
		t.Errorf("classifier disabled: expected base prompt unchanged")
	}
	if hinted {
		t.Errorf("classifier disabled: hinted should be false")
	}
	if trace.Mode != D10RouteBase {
		t.Errorf("classifier disabled: expected mode=base, got %s", trace.Mode)
	}
}

func TestBuildAnswerEnhanceSystemPrompt_SoftPath(t *testing.T) {
	t.Setenv("MEMDB_D10_CLASSIFIER_ENABLED", "true")
	// Push hard-routing threshold above 1 so we never trip the hard path,
	// regardless of what the hashed centroids produce. Forces soft.
	t.Setenv("MEMDB_D10_HARD_ROUTING_THRESHOLD", "1")
	emb := &fakeEmbedder{dim: 8, vec: func(s string) []float32 { return hashVec(8, "soft-path", s) }}
	got, hinted, trace := buildAnswerEnhanceSystemPrompt(context.Background(), "How many kids?", emb)
	if !hinted {
		t.Errorf("soft path: expected hinted=true")
	}
	if !strings.HasPrefix(got, answerEnhanceSystemPrompt) {
		t.Errorf("soft path: prompt should still start with base prompt")
	}
	if !strings.Contains(got, "Likely question type") {
		t.Errorf("soft path: missing distribution block")
	}
	if trace.Mode != D10RouteSoft {
		t.Errorf("soft path: expected mode=soft, got %s", trace.Mode)
	}
}

func TestBuildAnswerEnhanceSystemPrompt_HardPath(t *testing.T) {
	t.Setenv("MEMDB_D10_CLASSIFIER_ENABLED", "true")
	// Drop hard-routing threshold to 0 so any non-zero top-1 trips the hard
	// path. Then the prompt should be one of the hard category prompts (or
	// the base prompt if the dominant category has no hard prompt — open_domain
	// is the only such category, by design).
	t.Setenv("MEMDB_D10_HARD_ROUTING_THRESHOLD", "0")
	emb := &fakeEmbedder{dim: 8, vec: func(s string) []float32 { return hashVec(8, "hard-path", s) }}
	got, hinted, _ := buildAnswerEnhanceSystemPrompt(context.Background(), "How many kids?", emb)
	if !hinted {
		t.Errorf("hard path: expected hinted=true")
	}
	// One of: a hard prompt (when top-1 ∈ {single,multi,temporal,adversarial})
	// or the base prompt (when top-1 == open_domain — falls through to soft).
	isHardPrompt := false
	for _, p := range hardCategoryPrompts {
		if got == p {
			isHardPrompt = true
			break
		}
	}
	isFallback := strings.HasPrefix(got, answerEnhanceSystemPrompt) // soft block path
	if !isHardPrompt && !isFallback {
		t.Errorf("hard path: prompt is neither a hard prompt nor base+soft block. Got prefix:\n%.120s",
			got)
	}
}

func TestHardCategoryPrompts_AllCategoriesExceptOpenDomain(t *testing.T) {
	want := map[QueryCategory]bool{
		QueryCategorySingleHop:   true,
		QueryCategoryMultiHop:    true,
		QueryCategoryTemporal:    true,
		QueryCategoryAdversarial: true,
	}
	if len(hardCategoryPrompts) != len(want) {
		t.Errorf("expected %d hard prompts, got %d", len(want), len(hardCategoryPrompts))
	}
	for cat := range want {
		if _, ok := hardCategoryPrompts[cat]; !ok {
			t.Errorf("missing hard prompt for category %s", cat)
		}
	}
	if _, ok := hardCategoryPrompts[QueryCategoryOpenDomain]; ok {
		t.Errorf("open_domain must NOT have a hard prompt — should fall through to soft")
	}
}
