// Package search — cross-encoder precompute lookup (M10 Stream 6).
//
// Pre-computed scores are written by the D3 reorganizer
// (scheduler.RunTreeReorgForCube → CE precompute pass) into
// Memory.properties->>'ce_score_topk' as a JSON array of
// {neighbor_id, score} entries sorted DESC by score.
//
// At search time we cannot pre-compute (query, doc) pairs because the query
// is a free-form user string. The pre-computed cache stores **memory-pair
// affinity**: for each memory M_i, the BGE-rerank score against M_i's
// top-K nearest neighbours by cosine. This lets us short-circuit the live
// rerank HTTP call when the search candidate set looks like a tight
// neighbourhood — i.e. all results in the rerank batch are neighbours of
// the strongest cosine match (treated as "anchor"). When that holds, we
// reorder by cached pairwise scores instead of paying the live CE round-
// trip (~100-400ms p95).
//
// Application policy:
//   - Anchor = items[0] (highest cosine after step 6 ReRankByCosine).
//   - For items[1:], try lookup of (anchor.id → item.id) score in the
//     anchor's ce_score_topk.
//   - All hits → bypass live CE, reorder using cached pair scores
//     (anchor stays at index 0, others sorted by cached score DESC).
//   - Any miss → fall back to live CE for the whole batch (current
//     behaviour). Mixing cached + live scores would corrupt the ranking
//     because the two scales drift after re-training.
package search

import (
	"context"
	"encoding/json"
	"os"

	"github.com/anatolykoptev/go-kit/rerank"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// cePrecomputeEnabled gates the lookup-first path. Default TRUE for
// v0.23.0 (M10 Stream 6 — release-blocking perf improvement).
//
// Set MEMDB_CE_PRECOMPUTE=false to disable lookup and force every CE
// rerank call to hit the live HTTP endpoint (used for A/B benchmarks).
func cePrecomputeEnabled() bool {
	return os.Getenv("MEMDB_CE_PRECOMPUTE") != "false"
}

// extractCEScoreTopK pulls the pre-computed neighbour map out of an item's
// metadata. Returns nil if the key is absent, malformed, or empty —
// caller treats this as a miss.
//
// Tolerates float64 / float32 / json.Number for the score field, which
// covers both the in-process map[string]any path and the JSON-decoded
// path from properties::text::jsonb.
func extractCEScoreTopK(item map[string]any) map[string]float32 {
	scores, _ := extractCEScoreTopKWithStale(item)
	return scores
}

// extractCEScoreTopKWithStale is like extractCEScoreTopK but also reports
// whether any entry in the cached array was malformed (empty neighbor_id or
// unparseable score). A partial-parse is the proxy for a stale cache entry:
// it means the stored JSON was written for a neighbour that no longer carries
// a valid ID — spec § 9 "stale pre-computed (neighbor expired)" path.
//
// Returns (nil, false) when the key is absent or the array is empty.
// Returns (nil, true) when the array was present but every entry was malformed.
// Returns (scores, true) when the array was partially valid (some entries ok, some not).
// Returns (scores, false) when all entries parsed cleanly.
func extractCEScoreTopKWithStale(item map[string]any) (map[string]float32, bool) {
	meta, ok := item["metadata"].(map[string]any)
	if !ok {
		return nil, false
	}
	raw, present := meta["ce_score_topk"]
	if !present || raw == nil {
		return nil, false
	}

	var entries []map[string]any
	switch v := raw.(type) {
	case []any:
		entries = make([]map[string]any, 0, len(v))
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				entries = append(entries, m)
			}
		}
	case []map[string]any:
		entries = v
	case string:
		// Some legacy paths leave the array as a JSON string nested
		// inside agtype text — decode lazily.
		if v == "" {
			return nil, false
		}
		_ = json.Unmarshal([]byte(v), &entries)
	default:
		return nil, false
	}

	if len(entries) == 0 {
		return nil, false
	}
	out := make(map[string]float32, len(entries))
	hasInvalid := false
	for _, e := range entries {
		nid, _ := e["neighbor_id"].(string)
		if nid == "" {
			hasInvalid = true
			continue
		}
		switch s := e["score"].(type) {
		case float64:
			out[nid] = float32(s)
		case float32:
			out[nid] = s
		case json.Number:
			f, err := s.Float64()
			if err == nil {
				out[nid] = float32(f)
			} else {
				hasInvalid = true
			}
		default:
			hasInvalid = true
		}
	}
	if len(out) == 0 {
		return nil, hasInvalid
	}
	return out, hasInvalid
}

// rerankMemoryItemsPrecomputed is the lookup-first wrapper around
// rerankMemoryItems. Returns the (possibly reordered) slice.
//
// Behaviour:
//   - Disabled by env, < 2 items, or empty anchor.id → defer to live CE.
//   - Anchor lacks ce_score_topk entirely → emit miss, defer to live CE.
//   - Any item missing from anchor's neighbour map → emit miss, defer to
//     live CE (mixing cached + live scores corrupts ranking).
//   - Every non-anchor item present in cache → reorder by cached scores
//     DESC, mark cross_encoder_reranked=true on each item, return early
//     without hitting the live HTTP path.
func rerankMemoryItemsPrecomputed(
	ctx context.Context,
	client *rerank.Client,
	query string,
	items []map[string]any,
) []map[string]any {
	if !cePrecomputeEnabled() || len(items) < 2 {
		return rerankMemoryItems(ctx, client, query, items)
	}
	anchorID, _ := items[0]["id"].(string)
	if anchorID == "" {
		return rerankMemoryItems(ctx, client, query, items)
	}

	scoreByID, hasStale := extractCEScoreTopKWithStale(items[0])
	if scoreByID == nil {
		if hasStale {
			// Cache was present but every entry was malformed — expired/stale
			// neighbours whose IDs are no longer valid. Emit stale and fall back.
			recordCEPrecompute(ctx, "stale")
		} else {
			recordCEPrecompute(ctx, "miss")
		}
		return rerankMemoryItems(ctx, client, query, items)
	}
	if hasStale {
		// Some entries parsed but others were malformed — partial stale cache.
		// Fall back to live CE to avoid a corrupted ranking from mixed scores.
		recordCEPrecompute(ctx, "stale")
		return rerankMemoryItems(ctx, client, query, items)
	}

	// Walk non-anchor items collecting cached scores. Any miss → fall back.
	cachedScores := make([]float32, len(items))
	cachedScores[0] = 1.0 // anchor scores 1.0 against itself by convention
	for i := 1; i < len(items); i++ {
		id, _ := items[i]["id"].(string)
		if id == "" {
			recordCEPrecompute(ctx, "miss")
			return rerankMemoryItems(ctx, client, query, items)
		}
		s, ok := scoreByID[id]
		if !ok {
			recordCEPrecompute(ctx, "miss")
			return rerankMemoryItems(ctx, client, query, items)
		}
		cachedScores[i] = s
	}

	// All items had cached pair scores. Reorder DESC by cached score, keep
	// anchor at its post-cosine position (it's already the strongest).
	type indexed struct {
		score float32
		item  map[string]any
	}
	rest := make([]indexed, 0, len(items)-1)
	for i := 1; i < len(items); i++ {
		rest = append(rest, indexed{score: cachedScores[i], item: items[i]})
	}
	// Stable insertion sort — n is small (typically ≤ 50).
	for i := 1; i < len(rest); i++ {
		j := i
		for j > 0 && rest[j-1].score < rest[j].score {
			rest[j-1], rest[j] = rest[j], rest[j-1]
			j--
		}
	}

	out := make([]map[string]any, 0, len(items))
	out = append(out, items[0])
	if meta, ok := items[0]["metadata"].(map[string]any); ok {
		// Anchor self-score by convention; existing relativity is
		// preserved because callers downstream already trust anchor's
		// relativity from cosine.
		meta["cross_encoder_reranked"] = true
	}
	for _, r := range rest {
		if meta, ok := r.item["metadata"].(map[string]any); ok {
			meta["relativity"] = float64(r.score)
			meta["cross_encoder_reranked"] = true
		}
		out = append(out, r.item)
	}

	recordCEPrecompute(ctx, "hit")
	return out
}

// recordCEPrecompute bumps the precompute outcome counter. Safe to call
// pre-init (searchMx() lazy-initialises and pre-registers all labels).
func recordCEPrecompute(ctx context.Context, outcome string) {
	mx := searchMx()
	if mx == nil || mx.CEPrecomputeHit == nil {
		return
	}
	mx.CEPrecomputeHit.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}
