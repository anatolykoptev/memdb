package rerank

// ce_precompute.go — precompute-cache helpers for CrossEncoder.
//
// fireOnPrecompute / cePrecomputeEnabled / extractCEScoreTopK live here so
// the cache-extraction concerns stay isolated from the HTTP transport
// (ce_live.go) and from the math fallback (ce_math_fallback.go). The
// dispatcher in ce.go::rerank consumes them.

import (
	"context"
	"encoding/json"
	"os"
)

// fireOnPrecompute invokes the OnPrecompute hook with the given outcome
// label. Outcomes used by the dispatcher: hit | partial | miss | stale.
func (ce CrossEncoder) fireOnPrecompute(ctx context.Context, outcome string) {
	if ce.OnPrecompute != nil {
		ce.OnPrecompute(ctx, outcome)
	}
}

// cePrecomputeEnabled gates the lookup-first path. Default TRUE.
// Set MEMDB_CE_PRECOMPUTE=false to force live every time (used for A/B).
func cePrecomputeEnabled() bool {
	return os.Getenv("MEMDB_CE_PRECOMPUTE") != "false"
}

// extractCEScoreTopK pulls the cached neighbour map out of an item's
// ce_score_topk metadata field. Returns (nil, false) when absent;
// (nil, true) when present but every entry malformed; (scores, true)
// for partial parse; (scores, false) when fully clean.
//
// Tolerates float64 / float32 / json.Number for the score field — covers
// in-process map[string]any and JSON-decoded paths.
func extractCEScoreTopK(item Item) (map[string]float32, bool) {
	raw, ok := item.GetMeta("ce_score_topk")
	if !ok || raw == nil {
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
