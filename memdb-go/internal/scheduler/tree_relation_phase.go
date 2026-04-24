package scheduler

// tree_relation_phase.go — D3 relation-detection phase of the tree reorg cycle.
//
// After episodic + semantic promotion, every newly-created parent node is a
// candidate for a directed A→B relation against its nearest peers (CAUSES /
// CONTRADICTS / SUPPORTS / RELATED). DetectRelationPair performs the LLM
// classification and writes the edge; this file is responsible for:
//
//   1. Env-gating the whole phase (MEMDB_D3_RELATION_DETECTION, default off).
//   2. Selecting top-k cosine nearest neighbours per parent, under a global
//      per-cycle budget (maxRelationPairs).
//   3. Dispatching DetectRelationPair and emitting per-outcome metrics.
//
// Split out of tree_manager.go both to keep that file ≤200 lines and to give
// the relation concern a dedicated unit (distinct LLM call, distinct metric
// dimension, distinct rollout flag).

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// parentInfo carries everything runRelationPhase needs about a freshly-created
// tier parent: identity, text (LLM summary), raw embedding for cosine scoring,
// and the tier label (episodic/semantic) for future diagnostics.
type parentInfo struct {
	ID        string
	Text      string
	Embedding []float32
	Tier      string
}

// relationCandidate is a scored directed pair (fromIdx→toIdx) awaiting LLM
// classification. Score is cosine similarity — higher is dispatched first so
// the LLM budget is spent on the strongest links.
type relationCandidate struct {
	fromIdx int
	toIdx   int
	score   float64
}

// runRelationPhase is the tier-3 entry point. Called from RunTreeReorgForCube
// after both tier-1 and tier-2 finish. No-op unless MEMDB_D3_RELATION_DETECTION
// is explicitly set to "true" — this is a separate gate from MEMDB_REORG_HIERARCHY
// so the feature can be rolled out independently.
func (r *Reorganizer) runRelationPhase(ctx context.Context, cubeID string, parents []parentInfo) {
	if os.Getenv("MEMDB_D3_RELATION_DETECTION") != "true" {
		return
	}
	if len(parents) < 2 {
		return
	}

	log := r.logger.With(
		slog.String("cube_id", cubeID),
		slog.String("component", "tree_reorg_relation"),
	)

	topK := relationTopK()
	budget := maxRelationPairs()
	candidates := selectRelationCandidates(parents, topK)
	if len(candidates) > budget {
		candidates = candidates[:budget]
	}
	log.Debug("relation phase: candidates selected",
		slog.Int("parents", len(parents)),
		slog.Int("candidates", len(candidates)),
		slog.Int("topK", topK),
		slog.Int("budget", budget),
	)

	for _, c := range candidates {
		select {
		case <-ctx.Done():
			log.Warn("relation phase: ctx cancelled mid-loop")
			return
		default:
		}
		from := parents[c.fromIdx]
		to := parents[c.toIdx]

		schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
			attribute.String("tier", "relation"),
			attribute.String("outcome", "relation_attempted"),
		))

		rel, _, err := r.DetectRelationPair(ctx, from.ID, from.Text, to.ID, to.Text)
		switch {
		case err != nil:
			log.Warn("relation phase: detector error",
				slog.String("from", from.ID),
				slog.String("to", to.ID),
				slog.Any("error", err),
			)
			schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
				attribute.String("tier", "relation"),
				attribute.String("outcome", "relation_error"),
			))
		case rel == "":
			schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
				attribute.String("tier", "relation"),
				attribute.String("outcome", "relation_skipped"),
			))
		default:
			schedMx().TreeReorg.Add(ctx, 1, metric.WithAttributes(
				attribute.String("tier", "relation"),
				attribute.String("outcome", fmt.Sprintf("relation_written_%s", rel)),
			))
		}
	}
}

// selectRelationCandidates returns directed pairs (i→j) where j is among i's
// top-k cosine nearest-neighbour other parents. Pairs are deduplicated so the
// same directed edge never appears twice, and the result is sorted by score
// descending so callers can apply a budget cap by truncating the slice.
//
// Mutual nearest neighbours produce BOTH (i→j) and (j→i) — CAUSES is directed,
// so we want the LLM to see both directions and classify independently.
func selectRelationCandidates(parents []parentInfo, topK int) []relationCandidate {
	n := len(parents)
	if n < 2 || topK < 1 {
		return nil
	}

	type peer struct {
		idx   int
		score float64
	}

	seen := make(map[string]struct{}, n*topK)
	out := make([]relationCandidate, 0, n*topK)

	for i := 0; i < n; i++ {
		if len(parents[i].Embedding) == 0 {
			continue
		}
		peers := make([]peer, 0, n-1)
		for j := 0; j < n; j++ {
			if i == j || len(parents[j].Embedding) == 0 {
				continue
			}
			s := cosineBetween(parents[i].Embedding, parents[j].Embedding)
			peers = append(peers, peer{idx: j, score: s})
		}
		sort.Slice(peers, func(a, b int) bool { return peers[a].score > peers[b].score })
		if len(peers) > topK {
			peers = peers[:topK]
		}
		for _, p := range peers {
			if parents[i].ID == parents[p.idx].ID {
				continue
			}
			key := parents[i].ID + "|" + parents[p.idx].ID
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, relationCandidate{fromIdx: i, toIdx: p.idx, score: p.score})
		}
	}

	sort.Slice(out, func(a, b int) bool { return out[a].score > out[b].score })
	return out
}
