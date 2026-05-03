package rerank

import (
	"context"
	"log/slog"
	"sort"
	"time"
	"unicode/utf8"
)

// defaultMaxDocs caps docs shipped to the server when MaxDocs is 0.
const defaultMaxDocs = 50

// respBodyLimit bounds response body read to avoid runaway allocations on a
// misbehaving server. Rerank responses are small JSON; 256 KB covers
// pathological top_n values.
const respBodyLimit = 256 * 1024

// Config configures a rerank client. Zero URL disables all calls.
// Deprecated: use NewClient with functional options (Opt) instead.
type Config struct {
	URL            string        // base URL, e.g. "http://embed-server:8082"
	Model          string        // model name in request body
	APIKey         string        // optional Bearer token (Cohere hosted providers)
	Timeout        time.Duration // per-request HTTP timeout (applied via context.WithTimeout, NOT http.Client.Timeout)
	MaxDocs        int           // cap on docs sent (0 → defaultMaxDocs)
	MaxCharsPerDoc int           // rune-aware truncation (0 disables)
}

// Doc is a query-document pair. ID is opaque, returned unchanged in Scored.
// Source is an optional label (e.g. "web", "wiki") used by WithSourceWeights.
type Doc struct {
	ID          string
	Text        string
	Source      string    // G2-client: optional; used by WithSourceWeights. Zero value treated as unweighted.
	EmbedVector []float32 // G5: optional; used by MathReranker for cosine similarity scoring.
}

// Scored pairs an input Doc with its relevance score from the reranker.
// OrigRank is the original index of this doc in the input slice.
type Scored struct {
	Doc
	Score    float32
	OrigRank int
}

// Client is the rerank HTTP client. Safe for concurrent use.
// v2: internally holds *cfgInternal; v1 New(cfg, logger) translates Config to options.
type Client struct {
	cfg    *cfgInternal
	logger *slog.Logger // kept for v1 compat; v1 logs via c.logger.Warn directly
}

// New returns a configured client using the v1 Config struct.
// logger=nil uses slog.Default().
// Deprecated: use NewClient(url, opts...) for new code.
//
// G1 note: the default retry policy (retry-on-5xx, 3 attempts) is now active
// for v1 callers. Opt out via NewClient(url, WithRetry(rerank.NoRetry)).
func New(cfg Config, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	opts := []Opt{
		WithModel(cfg.Model),
		WithAPIKey(cfg.APIKey),
		WithTimeout(cfg.Timeout),
		WithMaxDocs(cfg.MaxDocs),
		WithMaxCharsPerDoc(cfg.MaxCharsPerDoc),
	}
	c := NewClient(cfg.URL, opts...)
	c.logger = logger
	return c
}

// Available reports whether the client is configured to make calls.
func (c *Client) Available() bool {
	return c != nil && c.cfg != nil && c.cfg.url != ""
}

// Rerank returns docs sorted by cross-encoder relevance score (desc). Best-
// effort: any error returns input unchanged (preserving order, Score=0,
// OrigRank=i). Docs beyond MaxDocs are preserved as-is after the reranked
// head.
//
// Deprecated: use RerankWithResult for new code (typed Result with Status).
func (c *Client) Rerank(ctx context.Context, query string, docs []Doc) []Scored {
	res, _ := c.RerankWithResult(ctx, query, docs)
	if res == nil {
		out := make([]Scored, len(docs))
		for i, d := range docs {
			out[i] = Scored{Doc: d, OrigRank: i}
		}
		return out
	}
	return res.Scored
}

// rerankInternal executes the full rerank pipeline and returns a Result.
// Shared by RerankWithResult (and transitively the v1 Rerank shim).
//
// Pipeline order (G2-client):
//
//	truncate(tokens) → truncate(chars) → instruction prefix → POST →
//	Normalize(local) → SourceWeights → score histogram →
//	sort → Threshold → TopN → tail preserve
func (c *Client) rerankInternal(ctx context.Context, query string, docs []Doc, opts ...RerankOpt) *Result {
	// Resolve per-call options.
	callCfg := rerankCallCfg{}
	for _, o := range opts {
		o(&callCfg)
	}

	pass := func() []Scored {
		out := make([]Scored, len(docs))
		for i, d := range docs {
			out[i] = Scored{Doc: d, OrigRank: i}
		}
		return out
	}

	// DryRun: skip HTTP entirely.
	if callCfg.DryRun {
		return &Result{
			Scored: pass(),
			Status: StatusSkipped,
			Model:  c.cfgModel(),
		}
	}

	if len(docs) == 0 || c == nil || c.cfg == nil || c.cfg.url == "" {
		return &Result{
			Scored: pass(),
			Status: StatusSkipped,
			Model:  c.cfgModel(),
		}
	}

	maxDocs := c.cfg.maxDocs
	if maxDocs <= 0 {
		maxDocs = defaultMaxDocs
	}

	head := docs
	var tail []Doc
	if len(docs) > maxDocs {
		head = docs[:maxDocs]
		tail = docs[maxDocs:]
	}

	// Extract texts with optional token-aware then char truncation.
	texts := make([]string, len(head))
	for i, d := range head {
		t := d.Text
		if c.cfg.maxTokensPerDoc > 0 {
			truncated, before, after := truncateToTokens(t, c.cfg.maxTokensPerDoc)
			t = truncated
			if before != after {
				docID := d.ID
				safeCall(func() { c.cfg.observer.OnTruncate(ctx, docID, before, after) })
				recordTruncate(c.cfg.model, "tokens")
			}
		}
		if c.cfg.maxCharsPerDoc > 0 {
			beforeChars := utf8.RuneCountInString(t)
			t = truncateRunes(t, c.cfg.maxCharsPerDoc)
			if utf8.RuneCountInString(t) < beforeChars {
				recordTruncate(c.cfg.model, "chars")
			}
		}
		texts[i] = t
	}

	// Apply instruction prefixes (bge-v1.5 / E5 style).
	modQuery, modTexts := applyInstructions(query, texts, c.cfg.queryInstruction, c.cfg.docInstruction)

	// G4: full-batch cache lookup. Hit = all N head docs present in cache;
	// miss (even 1 absent) = fall through to HTTP for the full batch.
	// Cache key includes serverNormalize and instruction prefixes so that
	// Clients with different configs sharing a Cache cannot cross-contaminate.
	if c.cfg.cache != nil {
		if cachedScores := tryCacheFullBatchGet(ctx, c.cfg.cache, c.cfg.model, c.cfg.serverNormalize, c.cfg.queryInstruction, c.cfg.docInstruction, query, head, c.cfg.maxCharsPerDoc, c.cfg.maxTokensPerDoc); cachedScores != nil {
			safeCall(func() { c.cfg.observer.OnCacheHit(ctx, len(head)) })
			recordCacheHit(c.cfg.model)
			return c.finalizeScoredFromCache(ctx, cachedScores, head, tail, maxDocs, callCfg)
		}
		recordCacheMiss(c.cfg.model)
	}

	// Fire OnBeforeCall hook.
	safeCall(func() { c.cfg.observer.OnBeforeCall(ctx, modQuery, len(modTexts)) })

	start := time.Now()
	// G1: callCohereResilient wraps callCohere with retry + circuit breaker.
	resp, err := c.callCohereResilient(ctx, modQuery, modTexts)
	dur := time.Since(start)
	recordDuration(c.cfg.model, dur)

	if err != nil {
		if c.logger != nil {
			c.logger.Warn("rerank failed",
				slog.String("url", c.cfg.url),
				slog.String("model", c.cfg.model),
				slog.Int("docs", len(modTexts)),
				slog.Any("err", err),
			)
		}
		recordStatus(c.cfg.model, "error")
		scored := pass()
		safeCall(func() { c.cfg.observer.OnAfterCall(ctx, StatusDegraded, dur, len(scored)) })
		return &Result{
			Scored: scored,
			Status: StatusDegraded,
			Model:  c.cfg.model,
			Err:    err,
		}
	}
	recordStatus(c.cfg.model, "ok")

	// Build scored head in server-returned order. Missing docs keep score=0
	// and get sorted to tail of the head block.
	scores := make([]float32, len(head))
	seen := make([]bool, len(head))
	for _, r := range resp.Results {
		if r.Index < 0 || r.Index >= len(head) {
			continue // defensive
		}
		scores[r.Index] = float32(r.RelevanceScore)
		seen[r.Index] = true
	}

	// G4: cache populate after successful HTTP (raw server scores, pre-pipeline).
	if c.cfg.cache != nil {
		var setCount int
		for i, d := range head {
			if seen[i] {
				c.cfg.cache.Set(ctx, cacheKey(c.cfg.model, c.cfg.serverNormalize, c.cfg.queryInstruction, c.cfg.docInstruction, query, d.Text, c.cfg.maxCharsPerDoc, c.cfg.maxTokensPerDoc), scores[i])
				setCount++
			}
		}
		recordCacheSet(c.cfg.model, setCount)
	}

	// G2-client pipeline stages: Normalize → SourceWeights → score histogram.
	// Applied on the raw scores array (indexed by original doc position in head).
	//
	// G2-client fix: normalize ONLY over seen scores so an unseen 0 doesn't
	// poison the distribution (matters for MinMax/ZScore; identity for None).
	if c.cfg.normalizeMode != NormalizeNone {
		seenIdx := make([]int, 0, len(scores))
		seenVals := make([]float32, 0, len(scores))
		for i, s := range scores {
			if seen[i] {
				seenIdx = append(seenIdx, i)
				seenVals = append(seenVals, s)
			}
		}
		seenVals = Normalize(seenVals, c.cfg.normalizeMode)
		for k, i := range seenIdx {
			scores[i] = seenVals[k]
		}
	} else {
		scores = Normalize(scores, c.cfg.normalizeMode) // identity, keeps current behavior
	}
	scores = applySourceWeights(scores, head, c.cfg.sourceWeights)
	emitScoreDistribution(c.cfg.model, scores)

	// Sort indices by score desc (stable). Sort through a permutation so the
	// comparator reads from a stable scores array — NOT from shuffled items.
	order := make([]int, len(head))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		// Unseen docs go to the end of the reranked block.
		if seen[order[i]] != seen[order[j]] {
			return seen[order[i]]
		}
		return scores[order[i]] > scores[order[j]]
	})

	// G2-client: Threshold filter (applied post-sort on post-normalize+weight scores).
	if callCfg.Threshold > 0 {
		filtered := order[:0]
		var dropped int
		for _, idx := range order {
			if scores[idx] >= callCfg.Threshold {
				filtered = append(filtered, idx)
			} else {
				dropped++
			}
		}
		order = filtered
		if dropped > 0 {
			recordBelowThreshold(c.cfg.model, dropped)
		}
	}

	// G2-client: TopN cap (applied after Threshold).
	if callCfg.TopN > 0 && callCfg.TopN < len(order) {
		order = order[:callCfg.TopN]
	}

	out := make([]Scored, 0, len(docs))
	for _, origIdx := range order {
		out = append(out, Scored{
			Doc:      head[origIdx],
			Score:    scores[origIdx],
			OrigRank: origIdx,
		})
	}
	// Preserve tail in original order at the end.
	for i, d := range tail {
		out = append(out, Scored{
			Doc:      d,
			Score:    0,
			OrigRank: maxDocs + i,
		})
	}

	model := c.cfg.model
	if resp.Model != "" {
		model = resp.Model
	}
	safeCall(func() { c.cfg.observer.OnAfterCall(ctx, StatusOk, dur, len(out)) })
	return &Result{
		Scored: out,
		Status: StatusOk,
		Model:  model,
	}
}

// finalizeScoredFromCache runs the post-HTTP pipeline (normalize → weight →
// score histogram → sort → threshold → TopN → tail preserve) on cached raw
// scores. All cached docs are treated as "seen" since cache only stores scores
// from successful HTTP calls.
//
// Called on a full-batch cache hit to honor the current cfg and per-call opts.
func (c *Client) finalizeScoredFromCache(ctx context.Context, cachedScores []float32, head, tail []Doc, maxDocs int, callCfg rerankCallCfg) *Result {
	scores := make([]float32, len(head))
	copy(scores, cachedScores)

	// All cached docs are seen (cache only stores confirmed server scores).
	seen := make([]bool, len(head))
	for i := range seen {
		seen[i] = true
	}

	// Run the same G2-client pipeline: Normalize → SourceWeights → histogram.
	if c.cfg.normalizeMode != NormalizeNone {
		seenIdx := make([]int, 0, len(scores))
		seenVals := make([]float32, 0, len(scores))
		for i, s := range scores {
			if seen[i] {
				seenIdx = append(seenIdx, i)
				seenVals = append(seenVals, s)
			}
		}
		seenVals = Normalize(seenVals, c.cfg.normalizeMode)
		for k, i := range seenIdx {
			scores[i] = seenVals[k]
		}
	} else {
		scores = Normalize(scores, c.cfg.normalizeMode)
	}
	scores = applySourceWeights(scores, head, c.cfg.sourceWeights)
	emitScoreDistribution(c.cfg.model, scores)

	order := make([]int, len(head))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return scores[order[i]] > scores[order[j]]
	})

	if callCfg.Threshold > 0 {
		filtered := order[:0]
		var dropped int
		for _, idx := range order {
			if scores[idx] >= callCfg.Threshold {
				filtered = append(filtered, idx)
			} else {
				dropped++
			}
		}
		order = filtered
		if dropped > 0 {
			recordBelowThreshold(c.cfg.model, dropped)
		}
	}

	if callCfg.TopN > 0 && callCfg.TopN < len(order) {
		order = order[:callCfg.TopN]
	}

	totalDocs := len(order) + len(tail)
	out := make([]Scored, 0, totalDocs)
	for _, origIdx := range order {
		out = append(out, Scored{
			Doc:      head[origIdx],
			Score:    scores[origIdx],
			OrigRank: origIdx,
		})
	}
	for i, d := range tail {
		out = append(out, Scored{
			Doc:      d,
			Score:    0,
			OrigRank: maxDocs + i,
		})
	}

	safeCall(func() { c.cfg.observer.OnAfterCall(ctx, StatusOk, 0, len(out)) })
	return &Result{
		Scored: out,
		Status: StatusOk,
		Model:  c.cfg.model,
	}
}

// tryCacheFullBatchGet returns cached scores for all docs in head, or nil if
// any doc is missing from the cache (partial miss → fall through to HTTP).
// All inputs that affect the upstream server response are included in the key.
func tryCacheFullBatchGet(ctx context.Context, cache Cache, model, serverNormalize, queryInstr, docInstr, query string, head []Doc, maxCharsPerDoc, maxTokensPerDoc int) []float32 {
	scores := make([]float32, len(head))
	for i, d := range head {
		s, ok := cache.Get(ctx, cacheKey(model, serverNormalize, queryInstr, docInstr, query, d.Text, maxCharsPerDoc, maxTokensPerDoc))
		if !ok {
			return nil // partial miss — abort, fall through to HTTP
		}
		scores[i] = s
	}
	return scores
}

// callCohereResilient wraps callCohere with:
//  1. Circuit breaker check (if configured) — returns ErrCircuitOpen immediately if open.
//  2. Retry loop via retry.do (default: 3 attempts on 5xx, exp backoff).
//  3. Circuit breaker feedback (MarkSuccess/MarkFailure if configured).
//
// This is the single wrap point per G1 spec.
func (c *Client) callCohereResilient(ctx context.Context, query string, texts []string) (*cohereResponse, error) {
	cb := c.cfg.circuit

	// 1. Circuit breaker guard.
	if cb != nil && !cb.Allow() {
		recordGiveup(c.cfg.model, "circuit_open")
		return nil, ErrCircuitOpen
	}

	// 2. Retry loop.
	resp, err := do(ctx, c.cfg.retry, c.cfg.model, c.cfg.observer, func() (*cohereResponse, error) {
		return c.callCohere(ctx, query, texts)
	})

	// 3. Circuit breaker feedback.
	if cb != nil {
		if err != nil {
			cb.MarkFailure()
		} else {
			cb.MarkSuccess()
		}
	}

	return resp, err
}

// cfgModel returns model name safely (nil-safe helper for Result.Model).
func (c *Client) cfgModel() string {
	if c == nil || c.cfg == nil {
		return ""
	}
	return c.cfg.model
}

// truncateRunes returns the first maxRunes runes of s. UTF-8 safe.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}
