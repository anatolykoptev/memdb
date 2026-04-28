package search

// cross_encoder_precompute.go — backward-compatible wrapper for the
// CE precompute lookup path. Implementation moved to
// internal/search/rerank/ce.go (rerank.CrossEncoder strategy) in M11/R3.
//
// Legacy public-shape helpers stay here because ce_precompute_test.go
// asserts on extractCEScoreTopK / extractCEScoreTopKWithStale directly:
// preserving the symbols means the existing test suite passes UNCHANGED.

import (
	"context"
	"encoding/json"

	"github.com/anatolykoptev/go-kit/rerank"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	rerankpkg "github.com/anatolykoptev/memdb/memdb-go/internal/search/rerank"
)

// rerankMemoryItemsPrecomputed wraps rerank.CrossEncoder.Rerank so the
// existing entrypoint signature (and ce_precompute_test.go calls) stays
// stable. Behaviour parity preserved bit-for-bit.
func rerankMemoryItemsPrecomputed(
	ctx context.Context,
	client *rerank.Client,
	query string,
	items []map[string]any,
) []map[string]any {
	if len(items) == 0 {
		return items
	}
	adapted := adaptItems(items)
	out, _ := rerankpkg.CrossEncoder{
		Client:       client,
		OnLiveCall:   ceLiveHook,
		OnPrecompute: cePrecomputeHook,
	}.Rerank(ctx, query, adapted)
	return unadaptItems(out)
}

// cePrecomputeHook bumps the M10 Stream 6 outcome counter
// (memdb.search.ce_precompute_hit_total). Pre-existing dashboard series.
func cePrecomputeHook(ctx context.Context, outcome string) {
	mx := searchMx()
	if mx == nil || mx.CEPrecomputeHit == nil {
		return
	}
	mx.CEPrecomputeHit.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// extractCEScoreTopK returns the cached neighbour map for an item, or nil
// when absent / malformed. Public-shape helper retained for
// ce_precompute_test.go::TestExtractCEScoreTopK_HandlesShapes.
func extractCEScoreTopK(item map[string]any) map[string]float32 {
	scores, _ := extractCEScoreTopKWithStale(item)
	return scores
}

// extractCEScoreTopKWithStale is like extractCEScoreTopK but also reports
// whether any entry was malformed. Retained verbatim from pre-R3 because
// ce_precompute_test.go::TestRerankMemoryItemsPrecomputed_StaleNeighbour
// + TestExtractCEScoreTopKWithStale_AllMalformed assert on the (scores,
// hasStale) tuple shape.
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
