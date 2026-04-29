package rerank

import (
	"context"
	"math"
)

// applyMMR applies Maximal Marginal Relevance (MMR) to re-order docs for
// relevance+diversity tradeoff.
//
// Algorithm (greedy, O(n²)):
//  1. Start with the highest-relevance doc.
//  2. At each step, select the doc that maximises:
//     MMR(d) = lambda * relScore(d) - (1-lambda) * max_s∈S cosineSim(d, s)
//     where S is the set of already-selected docs.
//  3. Repeat until all docs are selected.
//
// lambda=1 → pure relevance order (same as sorting by relScores desc).
// lambda=0 → pure diversity (maximise dissimilarity to already-selected docs).
// lambda=0.5 → balanced (recommended default).
//
// The returned Scored[i].Score contains the ORIGINAL relevance score (from
// relScores), NOT the MMR score. This preserves caller threshold semantics.
//
// ctx cancellation is honored at the start of each outer iteration; on cancel
// the function returns whatever has been selected so far.
//
// Empty docs or empty relScores returns an empty slice without panic.
// lambda is clamped to [0, 1] internally.
func applyMMR(ctx context.Context, docs []Doc, relScores []float32, lambda float32) []Scored {
	n := len(docs)
	if n == 0 || len(relScores) == 0 {
		return []Scored{}
	}
	// Clamp lambda for safety.
	if lambda < 0 {
		lambda = 0
	}
	if lambda > 1 {
		lambda = 1
	}

	selected := make([]bool, n)
	result := make([]Scored, 0, n)

	for len(result) < n {
		// Honour context cancellation.
		if ctx.Err() != nil {
			break
		}

		bestIdx := -1
		var bestScore float32
		first := true

		for i := 0; i < n; i++ {
			if selected[i] {
				continue
			}

			// Compute max cosine similarity to already-selected docs.
			// Initialize to -MaxFloat32 so that the first observed sim wins
			// regardless of sign — handles antiparallel/orthogonal corpora where
			// all cross-sims may be negative.
			maxSim := float32(-math.MaxFloat32)
			for _, s := range result {
				sim := cosineSim(docs[i].EmbedVector, s.Doc.EmbedVector)
				if sim > maxSim {
					maxSim = sim
				}
			}

			mmrScore := lambda*relScores[i] - (1-lambda)*maxSim

			if first || mmrScore > bestScore {
				bestScore = mmrScore
				bestIdx = i
				first = false
			}
		}

		if bestIdx < 0 {
			break // all selected (shouldn't happen, but defensive)
		}

		selected[bestIdx] = true
		result = append(result, Scored{
			Doc:      docs[bestIdx],
			Score:    relScores[bestIdx], // preserve original relevance, NOT MMR score
			OrigRank: bestIdx,
		})
	}

	return result
}
