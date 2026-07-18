package search

// answer_enhance_classifier.go — D10 probabilistic query-type classifier.
//
// Why this exists:
//   PR #250 introduced a regex/keyword hybrid that hard-swapped the system
//   prompt per category. The classifier mis-routed cat-1 questions and the
//   swap was unforgiving (wrong category → wrong shape rules → tanked F1).
//   PR #251 reverted to a single tight extractive prompt.
//
//   Independent LLM-Judge re-score of the pre-revert runs showed F1 was a
//   misleading proxy: cefix4 (with the soft-hybrid prompt) scored LLM Judge
//   0.275 vs cefix3 0.225 (+22%). The hybrid direction was correct; the
//   execution (hard swap on a brittle regex classifier) was the bug.
//
// This file implements a softer, win-win architecture:
//   1. Classify the query by cosine-similarity to per-category centroids
//      built from a small set of curated multilingual anchor questions
//      (no LLM, just the existing query embedder — typically multilingual
//      e5 large at embed-server :8082).
//   2. Append a short hint block to the SAME extractive base prompt
//      instead of replacing it. The base prompt's SHORTEST-surface-form
//      discipline is preserved; the hint only nudges the model toward
//      category-appropriate shape and explicitly defers to the base when
//      conflicting.
//   3. If top-1 confidence is below threshold (default 0.5) or top-1 is
//      open_domain (no useful shape constraint), emit no hint at all —
//      the prompt collapses to current-main behaviour, perfect rollout
//      safety on classifier mis-fires.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// QueryCategory enumerates the LoCoMo-aligned question shapes the D10
// classifier distinguishes. Same five values as the deleted PR #250 hybrid
// — the categories themselves were correct, only the routing strategy was
// not.
type QueryCategory string

const (
	QueryCategorySingleHop   QueryCategory = "single_hop"
	QueryCategoryMultiHop    QueryCategory = "multi_hop"
	QueryCategoryTemporal    QueryCategory = "temporal"
	QueryCategoryOpenDomain  QueryCategory = "open_domain"
	QueryCategoryAdversarial QueryCategory = "adversarial"
)

// CategoryConfidence is one (category, score) pair — the score is the cosine
// similarity between the query vector and the category centroid, normalised
// to the [0, 1] range. Higher is more confident.
type CategoryConfidence struct {
	Category   QueryCategory
	Confidence float64
}

// classifierEmbedder is a tiny, package-local interface so the classifier
// does not pull the full embedder package and tests can stub it without
// implementing the four-method full Embedder. The real embedder.Embedder
// satisfies this implicitly.
type classifierEmbedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Anchor data and per-category hint strings live in answer_enhance_anchors.go
// — keeping the curated content separate from the classifier logic makes the
// editorial surface obvious (any anchor edit shifts every centroid).

// classifierCategoryOrder is a stable iteration order for centroids and for
// pre-registering metric label series. Keep in sync with QueryCategory*.
var classifierCategoryOrder = []QueryCategory{
	QueryCategorySingleHop,
	QueryCategoryMultiHop,
	QueryCategoryTemporal,
	QueryCategoryOpenDomain,
	QueryCategoryAdversarial,
}

// lazyEmbedClassifier holds the embedder reference and a sync.Once for
// centroid pre-computation. The first ClassifyTopN call triggers a single
// batch embed of every anchor (≤75 anchors), reduces them per category to
// a unit-norm centroid, and caches the result for the process lifetime.
//
// Re-using the centroids across requests is intentional — anchors are
// inline strings, the centroids are pure functions of the embedder, and the
// embedder model is fixed per process (multilingual-e5-large at
// embed-server). If the operator swaps the embedder, restart picks up the
// new centroids.
type lazyEmbedClassifier struct {
	emb       classifierEmbedder
	once      sync.Once
	initErr   error
	centroids map[QueryCategory][]float32
}

// newLazyEmbedClassifier constructs a classifier backed by emb. emb may be
// nil — ClassifyTopN then returns the safe no-signal sentinel and the
// caller falls back to the base prompt.
//
// Construction is cheap (no embed calls). The first ClassifyTopN call is
// the one that triggers the centroid embed batch. Use
// classifierForEmbedder() to get a process-cached instance instead — that's
// the right entry point for production code.
func newLazyEmbedClassifier(emb classifierEmbedder) *lazyEmbedClassifier {
	return &lazyEmbedClassifier{emb: emb}
}

// classifierCacheMu guards classifierCache. The cache key is the embedder
// interface value (typed pointer); two SearchService instances sharing the
// same embedder share the same centroids — desirable, since centroids are
// pure functions of the embedder model.
var (
	classifierCacheMu sync.Mutex
	classifierCache   = make(map[classifierEmbedder]*lazyEmbedClassifier)
)

// classifierForEmbedder returns a process-cached classifier for emb. nil
// emb is returned as a fresh nil-backed classifier (its ClassifyTopN
// always emits the no-signal sentinel) and is NOT cached, so production
// code can pass a temporarily-nil embedder without polluting the cache.
func classifierForEmbedder(emb classifierEmbedder) *lazyEmbedClassifier {
	if emb == nil {
		return newLazyEmbedClassifier(nil)
	}
	classifierCacheMu.Lock()
	defer classifierCacheMu.Unlock()
	if c, ok := classifierCache[emb]; ok {
		return c
	}
	c := newLazyEmbedClassifier(emb)
	classifierCache[emb] = c
	return c
}

// ClassifyTopN embeds query, scores it against every centroid, and returns
// up to n top categories sorted desc by confidence. n is clamped to
// [1, len(classifierCategoryOrder)].
//
// Failure modes (all return a single open_domain conf=0 entry — "no signal"):
//   - nil embedder
//   - centroid init error (anchor embed failed on first call)
//   - empty query embedding
//   - per-call embedder error
//
// "No signal" is intentionally indistinguishable from a confident
// open_domain classification — both should suppress the hint block.
func (c *lazyEmbedClassifier) ClassifyTopN(ctx context.Context, query string, n int) ([]CategoryConfidence, error) {
	noSignal := []CategoryConfidence{{Category: QueryCategoryOpenDomain, Confidence: 0}}
	if c == nil || c.emb == nil {
		return noSignal, nil
	}
	c.once.Do(func() { c.initErr = c.computeCentroids(ctx) })
	if c.initErr != nil {
		return noSignal, c.initErr
	}
	if strings.TrimSpace(query) == "" {
		return noSignal, nil
	}
	vecs, err := c.emb.Embed(ctx, []string{query})
	if err != nil {
		return noSignal, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return noSignal, nil
	}
	q := normalise(vecs[0])
	scores := make([]CategoryConfidence, 0, len(classifierCategoryOrder))
	for _, cat := range classifierCategoryOrder {
		centroid, ok := c.centroids[cat]
		if !ok || len(centroid) != len(q) {
			continue
		}
		// q and centroid are both unit-norm → cosine = dot product. Clamp
		// negatives to 0 so the confidence stays in [0, 1].
		sim := dot(q, centroid)
		if sim < 0 {
			sim = 0
		}
		scores = append(scores, CategoryConfidence{Category: cat, Confidence: sim})
	}
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Confidence > scores[j].Confidence
	})
	if n < 1 {
		n = 1
	}
	if n > len(scores) {
		n = len(scores)
	}
	if n == 0 {
		return noSignal, nil
	}
	return scores[:n], nil
}

// computeCentroids embeds every anchor in a single batch call (best for
// embed-server batching), normalises each vector to unit length, then
// averages per category and re-normalises. Idempotent under sync.Once —
// callers that race the first call all see the same centroids.
func (c *lazyEmbedClassifier) computeCentroids(ctx context.Context) error {
	if c.emb == nil {
		return fmt.Errorf("classifier: nil embedder")
	}
	// Flatten anchors into a single slice with an index map back to the
	// category so a single batched Embed call covers every anchor.
	var (
		texts []string
		owner []QueryCategory
	)
	for _, cat := range classifierCategoryOrder {
		for _, a := range anchorQuestions[cat] {
			texts = append(texts, a)
			owner = append(owner, cat)
		}
	}
	if len(texts) == 0 {
		return fmt.Errorf("classifier: empty anchor set")
	}
	vecs, err := c.emb.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("classifier: anchor embed: %w", err)
	}
	if len(vecs) != len(texts) {
		return fmt.Errorf("classifier: anchor embed returned %d vectors, expected %d", len(vecs), len(texts))
	}
	dim := len(vecs[0])
	if dim == 0 {
		return fmt.Errorf("classifier: anchor embed returned empty vectors")
	}
	sums := make(map[QueryCategory][]float32, len(classifierCategoryOrder))
	counts := make(map[QueryCategory]int, len(classifierCategoryOrder))
	for i, v := range vecs {
		if len(v) != dim {
			return fmt.Errorf("classifier: inconsistent embedding dimension %d vs %d", len(v), dim)
		}
		cat := owner[i]
		if _, ok := sums[cat]; !ok {
			sums[cat] = make([]float32, dim)
		}
		nv := normalise(v)
		for j := range nv {
			sums[cat][j] += nv[j]
		}
		counts[cat]++
	}
	c.centroids = make(map[QueryCategory][]float32, len(sums))
	for cat, s := range sums {
		n := counts[cat]
		if n == 0 {
			continue
		}
		inv := 1 / float32(n)
		for j := range s {
			s[j] *= inv
		}
		c.centroids[cat] = normalise(s)
	}
	return nil
}

// normalise returns a unit-length copy of v. Zero vectors come back
// unchanged (avoids NaN in cosine).
func normalise(v []float32) []float32 {
	out := make([]float32, len(v))
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		copy(out, v)
		return out
	}
	inv := float32(1 / math.Sqrt(sum))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// dot computes the inner product of two equal-length slices. No length
// check — the caller guarantees equal dimension.
func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// categoryHintBlock — see answer_enhance_hint.go (pure prompt-shape concern,
// kept separate from the classifier math).
