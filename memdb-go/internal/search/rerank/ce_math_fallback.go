package rerank

// ce_math_fallback.go — CrossEncoder → MathReranker fallback (+ pre-bypass).
//
// Three failure modes of the live CE call motivate this file:
//
//  1. Availability — ceClient returns StatusDegraded (timeout, 5xx,
//     ErrCircuitOpen). Pattern from go-search PR #10.
//  2. Quality — ceClient returns StatusOk but the top-1 score is below
//     MEMDB_CE_QUALITY_FLOOR. gte-multi-rerank hallucinates near-zero
//     scores on narrow OOD queries. Pattern from go-search PR #14.
//  3. Spread — top-1 / top-2 ratio < MEMDB_CE_SPREAD_FLOOR (default 1.5).
//     CE returns scores all clustered near zero — no relevance separation.
//     Math fallback gives better ranking on these queries. NEW 2026-05-02.
//
// Plus a PRE-BYPASS optimization (NEW 2026-05-02): if cosine top-1 across
// the input docs is already > MEMDB_CE_BYPASS_COSINE_THRESHOLD (default 0
// = disabled; recommended 0.85), skip the live CE call entirely and return
// math scores. Saves ~500ms/query on high-confidence retrieval where CE
// would only re-confirm the cosine ranking. Forensic 2026-05-02 found 86%
// of CE calls dispatched to math fallback anyway — pre-bypass eliminates
// the wasted CE compute.
//
// Without a fallback the require_positive_ce / threshold_filter gate
// downstream drops every result on either failure. With it, the chat
// pipeline keeps producing positive-cosine scores from vectors the
// embedder already gave us — zero extra embed-server load.

import (
	"context"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-kit/rerank"
)

const (
	// ceQualityFloorEnv gates the low-quality CE → MathReranker fallback.
	// CE returning StatusOk with top-1 score below this floor is treated
	// as a hallucination and routes through cosine. Set to 0 to disable
	// the quality gate (preserve pre-#14 behaviour).
	//
	// 2026-05-02 — default lowered 0.1 → 0.01 after embed-server flipped
	// to sigmoid normalization by default. Sigmoid range is [0,1]; 0.01
	// equals raw logit ≈ -4.6 (hard negative). The old 0.1 default was
	// tuned for raw-logit scale [-10,+10] where 0.1 ~ logit -2.2 (soft
	// negative); on sigmoid scale the same threshold caught all near-zero
	// CE outputs and routed 86% of calls through math fallback (Run #13
	// forensic). Lower default keeps the gate sharp for genuine
	// hallucinations only.
	ceQualityFloorEnv     = "MEMDB_CE_QUALITY_FLOOR"
	ceQualityFloorDefault = 0.01

	// ceSpreadFloorEnv triggers the low-quality fallback when CE returns
	// scores with insufficient spread (top-1 / top-2 < floor). Value 1.0
	// (default) disables — top-1 must beat top-2 by ≥1.0× to be considered
	// "discriminating". Recommended 1.5 (top-1 50% above runner-up).
	//
	// Rationale: CE returning [0.06, 0.05, 0.04] passes the absolute floor
	// (0.06 > 0.01) but provides zero ranking signal — math cosine on the
	// same docs usually picks a clearer winner. Spread floor catches this
	// case the absolute floor misses.
	ceSpreadFloorEnv     = "MEMDB_CE_SPREAD_FLOOR"
	ceSpreadFloorDefault = 1.0 // disabled by default; opt-in via env

	// ceBypassCosineThresholdEnv enables the pre-bypass optimization. When
	// cosine top-1 (computed from QueryVec + EmbeddingsByID, already loaded)
	// exceeds this threshold, the live CE call is skipped entirely — math
	// cosine handles the ranking. Saves ~500ms/query on high-confidence
	// retrieval where CE would only re-confirm cosine order.
	//
	// Default 0 = disabled (preserve legacy "always call CE" behaviour).
	// Recommended 0.85 — gte-multi-rerank rarely overturns cosine top-1
	// when cosine confidence is that high; the per-query CE compute is
	// pure waste on those queries.
	ceBypassCosineThresholdEnv     = "MEMDB_CE_BYPASS_COSINE_THRESHOLD"
	ceBypassCosineThresholdDefault = 0.0
)

// ceQualityFloor reads MEMDB_CE_QUALITY_FLOOR; returns the default on
// missing / malformed / negative values.
func ceQualityFloor() float64 {
	return parseFloatEnv(ceQualityFloorEnv, ceQualityFloorDefault)
}

// ceSpreadFloor reads MEMDB_CE_SPREAD_FLOOR; returns the default on
// missing / malformed / out-of-range values. Range [1.0, 100.0].
func ceSpreadFloor() float64 {
	v := parseFloatEnv(ceSpreadFloorEnv, ceSpreadFloorDefault)
	if v < 1.0 {
		return ceSpreadFloorDefault
	}
	return v
}

// ceBypassCosineThreshold reads MEMDB_CE_BYPASS_COSINE_THRESHOLD; returns
// the default on missing / malformed / out-of-range values. Range [0, 1].
// 0 = disabled (no pre-bypass).
func ceBypassCosineThreshold() float64 {
	v := parseFloatEnv(ceBypassCosineThresholdEnv, ceBypassCosineThresholdDefault)
	if v < 0 || v > 1 {
		return ceBypassCosineThresholdDefault
	}
	return v
}

// parseFloatEnv reads an env var as float64. Returns def on empty / parse
// error / negative. Bounds-checking is callee's responsibility.
func parseFloatEnv(name string, def float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return def
	}
	return v
}

// rerankWithMathFallback is the CE-or-cosine wrapper. Three paths:
//
//  1. Pre-bypass (NEW 2026-05-02): if cosine top-1 from QueryVec exceeds
//     MEMDB_CE_BYPASS_COSINE_THRESHOLD (default 0=disabled), skip live CE
//     entirely and return math scores. OnMathFallback fires with reason
//     "bypass_cosine".
//  2. Healthy: live CE returns StatusOk + scores pass quality+spread floors
//     → return CE scores directly.
//  3. Degraded / low-quality / poor-spread: route through MathReranker
//     (cosine on QueryVec). OnMathFallback fires with reason
//     "degraded" | "low_quality" | "low_spread".
//
// Returns nil only when the live path failed AND no QueryVec — caller is
// expected to treat nil as "no scores, leave items as-is" (callers in
// ce_live.go already do).
func (ce CrossEncoder) rerankWithMathFallback(ctx context.Context, query string, docs []rerank.Doc) []rerank.Scored {
	// Path 1 — pre-bypass on cosine confidence. Cheap (cosine already
	// computed by retrieval); only fires when we have QueryVec to bypass to.
	bypassFloor := ceBypassCosineThreshold()
	if bypassFloor > 0 && len(ce.QueryVec) > 0 && len(docs) > 0 {
		topCosine := topCosineScore(ce.QueryVec, docs)
		if topCosine >= float32(bypassFloor) {
			slog.Debug("ce_rerank: pre-bypass via cosine confidence",
				slog.Float64("top_cosine", float64(topCosine)),
				slog.Float64("threshold", bypassFloor),
			)
			if ce.OnMathFallback != nil {
				ce.OnMathFallback(ctx, "bypass_cosine")
			}
			return mathRerankCosine(ce.QueryVec, docs)
		}
	}

	// Per-cube circuit breaker (Karpathy r2). Cubes whose recent CE
	// history shows >50% low_spread outcomes skip the live HTTP call
	// entirely — math fallback handles the ranking. Self-recovers when
	// fresh outcomes drop the rate. Empty QueryCubeID disables the check.
	if globalCECircuit.shouldSkipCE(ce.QueryCubeID) && len(ce.QueryVec) > 0 && len(docs) > 0 {
		slog.Debug("ce_rerank: per-cube circuit open, skipping live CE",
			slog.String("cube_id", ce.QueryCubeID),
		)
		if ce.OnMathFallback != nil {
			ce.OnMathFallback(ctx, "circuit_open")
		}
		return mathRerankCosine(ce.QueryVec, docs)
	}

	// Path 2/3 — live CE call.
	res, err := ce.Client.RerankWithResult(ctx, query, docs)

	ceDegraded := res == nil || res.Status == rerank.StatusDegraded
	ceLowQuality := false
	ceLowSpread := false

	if !ceDegraded && len(res.Scored) > 0 {
		floor := ceQualityFloor()
		spreadFloor := ceSpreadFloor()

		// Compute top-1 and top-2 in one pass.
		var top1, top2 float64
		for _, s := range res.Scored {
			score := float64(s.Score)
			switch {
			case score > top1:
				top2 = top1
				top1 = score
			case score > top2:
				top2 = score
			}
		}

		if floor > 0 {
			ceLowQuality = top1 < floor
		}
		// Spread check only when both scores are non-trivial; avoids
		// 0/0 noise on tiny batches or all-zero CE output.
		if !ceLowQuality && spreadFloor > 1.0 && top1 > 0 && top2 > 0 {
			ceLowSpread = (top1 / top2) < spreadFloor
		}
	}

	// Feed the per-cube circuit breaker. We record on every live CE call
	// (including healthy ones) so the rate reflects real recent history
	// and the breaker self-recovers when CE quality returns.
	globalCECircuit.recordOutcome(ce.QueryCubeID, ceLowSpread)

	if !ceDegraded && !ceLowQuality && !ceLowSpread {
		return res.Scored
	}

	// No query vector → can't run math fallback. Return what CE gave (if
	// anything) so the caller still has a stable shape to work with.
	if len(ce.QueryVec) == 0 {
		if res != nil {
			return res.Scored
		}
		return nil
	}

	reason := "degraded"
	switch {
	case ceLowQuality:
		reason = "low_quality"
	case ceLowSpread:
		reason = "low_spread"
	}
	slog.Warn("ce_rerank: falling back to MathReranker",
		slog.String("reason", reason),
		slog.Any("error", err),
	)
	if ce.OnMathFallback != nil {
		ce.OnMathFallback(ctx, reason)
	}
	return mathRerankCosine(ce.QueryVec, docs)
}

// topCosineScore returns the maximum cosine similarity between queryVec
// and any doc's EmbedVector. Docs without an EmbedVector (or mismatched
// dim) contribute 0. Returns 0 on empty input.
func topCosineScore(queryVec []float32, docs []rerank.Doc) float32 {
	if len(queryVec) == 0 || len(docs) == 0 {
		return 0
	}
	var top float32
	for _, d := range docs {
		if len(d.EmbedVector) != len(queryVec) {
			continue
		}
		s := cosineFloat32(queryVec, d.EmbedVector)
		if s > top {
			top = s
		}
	}
	return top
}

// mathRerankCosine reorders docs by cosine similarity to queryVec. Docs
// without an EmbedVector (or with mismatched dim) get score 0 and sink to
// the bottom in their original relative order. Drop-in replacement for
// ce.Client.Rerank when the live path hallucinates or fails.
func mathRerankCosine(queryVec []float32, docs []rerank.Doc) []rerank.Scored {
	if len(queryVec) == 0 || len(docs) == 0 {
		return nil
	}
	scored := make([]rerank.Scored, 0, len(docs))
	for i, d := range docs {
		score := float32(0)
		if len(d.EmbedVector) == len(queryVec) {
			score = cosineFloat32(queryVec, d.EmbedVector)
		}
		scored = append(scored, rerank.Scored{Doc: d, Score: score, OrigRank: i})
	}
	// Insertion sort DESC by score — N is bounded by chat top-K (≤30 typical).
	for i := 1; i < len(scored); i++ {
		j := i
		for j > 0 && scored[j-1].Score < scored[j].Score {
			scored[j-1], scored[j] = scored[j], scored[j-1]
			j--
		}
	}
	return scored
}

// cosineFloat32 is a minimal pure-Go cosine. Returns 0 on zero-norm or
// length-mismatched input. Local to avoid pulling go-kit/rerank's
// MathReranker (which expects different input types) for one helper.
func cosineFloat32(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(float64(dot) / math.Sqrt(float64(na)*float64(nb)))
}
