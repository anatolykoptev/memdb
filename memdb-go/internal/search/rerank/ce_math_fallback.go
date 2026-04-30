package rerank

// ce_math_fallback.go — CrossEncoder → MathReranker fallback.
//
// Two failure modes of gte-multi-rerank running on embed-server motivate
// this file:
//
//  1. Availability — ceClient returns StatusDegraded (timeout, 5xx,
//     ErrCircuitOpen). Pattern from go-search PR #10.
//  2. Quality — ceClient returns StatusOk but the top-1 score is below
//     MEMDB_CE_QUALITY_FLOOR (default 0.1). gte-multi-rerank hallucinates
//     near-zero scores on narrow OOD English queries even when cosine
//     similarity is 0.9+. Pattern from go-search PR #14.
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
	ceQualityFloorEnv     = "MEMDB_CE_QUALITY_FLOOR"
	ceQualityFloorDefault = 0.1
)

// ceQualityFloor reads MEMDB_CE_QUALITY_FLOOR; returns the default on
// missing / malformed / negative values.
func ceQualityFloor() float64 {
	raw := strings.TrimSpace(os.Getenv(ceQualityFloorEnv))
	if raw == "" {
		return ceQualityFloorDefault
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return ceQualityFloorDefault
	}
	return v
}

// rerankWithMathFallback is the CE-or-cosine wrapper. Healthy path returns
// CE scores directly; degraded or low-quality responses route through
// MathReranker when QueryVec is set. Returns nil only when CE was degraded
// AND no QueryVec — caller is expected to treat nil as "no scores, leave
// items as-is" (callers in ce_live.go already do).
func (ce CrossEncoder) rerankWithMathFallback(ctx context.Context, query string, docs []rerank.Doc) []rerank.Scored {
	res, err := ce.Client.RerankWithResult(ctx, query, docs)

	ceDegraded := res == nil || res.Status == rerank.StatusDegraded
	ceLowQuality := false
	floor := ceQualityFloor()
	if !ceDegraded && floor > 0 && len(res.Scored) > 0 {
		topScore := float64(res.Scored[0].Score)
		for _, s := range res.Scored {
			if float64(s.Score) > topScore {
				topScore = float64(s.Score)
			}
		}
		ceLowQuality = topScore < floor
	}

	if !ceDegraded && !ceLowQuality {
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
	if ceLowQuality {
		reason = "low_quality"
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
