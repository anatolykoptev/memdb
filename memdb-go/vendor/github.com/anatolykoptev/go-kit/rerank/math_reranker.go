package rerank

import (
	"context"
	"sort"
)

// Compile-time assertion: MathReranker implements Reranker.
var _ Reranker = MathReranker{}

// MathReranker is a pure-Go Reranker that scores documents using cosine
// similarity between a caller-supplied QueryVector and each Doc.EmbedVector.
// Optional MMR diversity is applied when Lambda > 0.
//
// Caller computes QueryVector via embed-server /v1/embeddings and sets
// EmbedVector on each Doc before passing to Rerank/RerankWithResult.
//
// Lambda controls the relevance/diversity tradeoff for MMR:
//   - 0  → pure cosine sort (no MMR).
//   - 0.5 → balanced (recommended default).
//   - 1  → MMR with pure relevance (equivalent to pure cosine sort, but via MMR loop).
//
// Zero-value MathReranker{} is valid — Available() returns false when
// QueryVector is empty, and Rerank/RerankWithResult return StatusSkipped passthrough.
type MathReranker struct {
	// QueryVector is the embedding of the query. Required — empty vector disables scoring.
	QueryVector []float32
	// Lambda controls MMR relevance-vs-diversity tradeoff (standard
	// Carbonell-Goldstein 1998 convention):
	//   1.0 = pure relevance (MMR equivalent of pure cosine sort)
	//   0.5 = balanced relevance/diversity (recommended default for diversity)
	//   0.0 = pure diversity (skip MMR; pure cosine sort fast path)
	//
	// Default 0 → no MMR; relScores sorted desc. Set Lambda > 0 to engage MMR.
	Lambda float32
}

// Available reports true if QueryVector is non-empty.
func (m MathReranker) Available() bool {
	return len(m.QueryVector) > 0
}

// Rerank satisfies the Reranker interface. Delegates to RerankWithResult and
// returns res.Scored. Errors are swallowed (same pattern as *Client.Rerank).
func (m MathReranker) Rerank(ctx context.Context, _ string, docs []Doc) []Scored {
	res, _ := m.RerankWithResult(ctx, "", docs)
	if res == nil {
		out := make([]Scored, len(docs))
		for i, d := range docs {
			out[i] = Scored{Doc: d, OrigRank: i}
		}
		return out
	}
	return res.Scored
}

// RerankWithResult scores docs by cosine similarity to QueryVector, optionally
// applying MMR diversity reranking (Lambda > 0).
//
// Passthrough paths (StatusSkipped, no compute):
//   - QueryVector is empty.
//   - docs is empty.
//   - WithDryRun() opt is set.
//
// Docs with empty EmbedVector receive score 0 and are sorted to the bottom
// of the list. An empty-vector count metric is emitted when any are found.
func (m MathReranker) RerankWithResult(ctx context.Context, _ string, docs []Doc, opts ...RerankOpt) (*Result, error) {
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

	// DryRun: skip all compute, return passthrough.
	if callCfg.DryRun {
		return &Result{
			Scored: pass(),
			Status: StatusSkipped,
			Model:  "math",
		}, nil
	}

	// Empty QueryVector or empty docs → passthrough.
	if len(m.QueryVector) == 0 || len(docs) == 0 {
		return &Result{
			Scored: pass(),
			Status: StatusSkipped,
			Model:  "math",
		}, nil
	}

	// Compute cosine similarity for each doc.
	relScores := make([]float32, len(docs))
	var emptyVecCount int
	for i, d := range docs {
		if len(d.EmbedVector) == 0 {
			emptyVecCount++
			relScores[i] = 0
		} else {
			relScores[i] = cosineSim(m.QueryVector, d.EmbedVector)
		}
	}

	if emptyVecCount > 0 {
		recordMathEmptyVector(emptyVecCount)
	}

	// Emit score distribution metric for all computed scores.
	emitMathScoreDistribution(relScores)

	var scored []Scored

	if m.Lambda > 0 {
		// MMR path: greedy diversity-aware selection.
		recordMathMMRApplied()
		scored = applyMMR(ctx, docs, relScores, m.Lambda)
	} else {
		// Pure cosine sort: build Scored, sort desc by relScore.
		scored = make([]Scored, len(docs))
		for i, d := range docs {
			scored[i] = Scored{
				Doc:      d,
				Score:    relScores[i],
				OrigRank: i,
			}
		}
		sort.SliceStable(scored, func(i, j int) bool {
			return scored[i].Score > scored[j].Score
		})
	}

	return &Result{
		Scored: scored,
		Status: StatusOk,
		Model:  "math",
	}, nil
}
