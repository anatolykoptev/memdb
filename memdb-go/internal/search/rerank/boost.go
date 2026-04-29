// Package rerank — metadata-driven boost weights (Q3 stream).
//
// Ported from MemOS upstream `_apply_boost_generic` (Q3 stream).
// After the cross-encoder scores are assigned, applyMetadataBoost
// multiplies each item's score by (1 + sum_of_matched_weights):
//
//   - user_id match → +BoostWeights.UserID    (default 0.5)
//   - session_id match → +BoostWeights.SessionID (default 0.3)
//   - tags overlap → +TagsEach per matched tag, capped at TagsCap
//                    (defaults 0.2 / 0.6)
//
// Zero-weight and empty-string query keys are explicit no-ops —
// no boost is applied when the query carries no user/session context.
package rerank

import (
	"context"
	"math"
	"os"
	"strconv"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// BoostWeights holds the per-factor multiplier configuration.
// All fields must be ≥ 0. A weight of 0 disables that factor.
type BoostWeights struct {
	// UserID is added to the total boost when the item's user_id
	// matches the query's user_id. Default 0.5 (MemOS parity).
	UserID float64
	// SessionID is added when item's session_id matches query's. Default 0.3.
	SessionID float64
	// TagsEach is added per overlapping tag. Default 0.2.
	TagsEach float64
	// TagsCap is the maximum cumulative boost from tag overlap. Default 0.6.
	TagsCap float64
}

// DefaultBoostWeights reads env overrides; falls back to MemOS defaults.
//
//	MEMDB_RERANK_BOOST_USER_ID    → UserID    (default 0.5)
//	MEMDB_RERANK_BOOST_SESSION_ID → SessionID (default 0.3)
//	MEMDB_RERANK_BOOST_TAGS       → TagsEach  (default 0.2)
//	MEMDB_RERANK_BOOST_TAGS_CAP   → TagsCap   (default 0.6)
func DefaultBoostWeights() BoostWeights {
	return BoostWeights{
		UserID:    envFloat("MEMDB_RERANK_BOOST_USER_ID", 0.5),
		SessionID: envFloat("MEMDB_RERANK_BOOST_SESSION_ID", 0.3),
		TagsEach:  envFloat("MEMDB_RERANK_BOOST_TAGS", 0.2),
		TagsCap:   envFloat("MEMDB_RERANK_BOOST_TAGS_CAP", 0.6),
	}
}

// applyMetadataBoost multiplies each item's score by (1 + boost) where
// boost is the sum of all matched weight factors. Items with zero boost
// are unchanged. Items that receive a boost get a "metadata_boost" meta
// key stamped for observability.
//
// This function is a pure multiplicative post-step on already-scored
// items — it MUST NOT change ordering logic or CE score values.
func applyMetadataBoost(
	ctx context.Context,
	items []Item,
	queryUserID, querySessionID string,
	queryTags []string,
	w BoostWeights,
) {
	mx := boostMx()
	for _, it := range items {
		boost := 0.0

		// user_id: empty query string is NOT a wildcard — no boost.
		if queryUserID != "" && w.UserID != 0 {
			if uid, ok := it.GetMeta("user_id"); ok && uid == queryUserID {
				boost += w.UserID
				mx.BoostApplied.Add(ctx, 1, metric.WithAttributes(
					attribute.String("factor", "user_id"),
				))
			}
		}

		// session_id: same empty-string guard.
		if querySessionID != "" && w.SessionID != 0 {
			if sid, ok := it.GetMeta("session_id"); ok && sid == querySessionID {
				boost += w.SessionID
				mx.BoostApplied.Add(ctx, 1, metric.WithAttributes(
					attribute.String("factor", "session_id"),
				))
			}
		}

		// tags: count overlapping tags, cap the accumulated boost.
		if len(queryTags) > 0 && w.TagsEach != 0 {
			if rawTags, ok := it.GetMeta("tags"); ok && rawTags != nil {
				overlap := countTagOverlap(rawTags, queryTags)
				if overlap > 0 {
					tagBoost := math.Min(float64(overlap)*w.TagsEach, w.TagsCap)
					boost += tagBoost
					mx.BoostApplied.Add(ctx, 1, metric.WithAttributes(
						attribute.String("factor", "tags"),
					))
				}
			}
		}

		if boost == 0 {
			continue
		}
		it.SetScore(it.Score() * (1.0 + boost))
		it.SetMeta("metadata_boost", boost)
	}
}

// countTagOverlap returns the number of tags in itemTags that appear in
// queryTags. itemTags is the raw value from Item.GetMeta("tags") which
// may be []string, []any, or a single string.
func countTagOverlap(itemTags any, queryTags []string) int {
	if len(queryTags) == 0 {
		return 0
	}
	// Build a set from queryTags for O(1) lookup.
	qset := make(map[string]struct{}, len(queryTags))
	for _, t := range queryTags {
		qset[t] = struct{}{}
	}

	count := 0
	switch v := itemTags.(type) {
	case []string:
		for _, t := range v {
			if _, ok := qset[t]; ok {
				count++
			}
		}
	case []any:
		for _, t := range v {
			if s, ok := t.(string); ok {
				if _, ok := qset[s]; ok {
					count++
				}
			}
		}
	case string:
		if _, ok := qset[v]; ok {
			count++
		}
	}
	return count
}

// envFloat reads key from env as float64; returns def on missing or parse error.
// Also used by mmr.go in this package (see comment there).
func envFloat(key string, def float64) float64 {
	s := os.Getenv(key)
	if s == "" {
		return def
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return v
}

// boostMetricsInstruments holds OTel counters for the boost step.
type boostMetricsInstruments struct {
	BoostApplied metric.Int64Counter
}

var (
	boostMxInst *boostMetricsInstruments
	boostMxOnce sync.Once
)

func boostMx() *boostMetricsInstruments {
	boostMxOnce.Do(func() {
		m := otel.Meter("memdb-go/search/rerank")
		c, _ := m.Int64Counter("memdb.search.rerank_boost_applied_total",
			metric.WithDescription("Per-factor metadata boost applied during CE rerank (factor in user_id|session_id|tags)"))
		inst := &boostMetricsInstruments{BoostApplied: c}
		// Pre-register all factor labels at zero so Prometheus scrapes see
		// the series from container start even before any boost fires.
		ctx := context.Background()
		for _, factor := range []string{"user_id", "session_id", "tags"} {
			c.Add(ctx, 0, metric.WithAttributes(attribute.String("factor", factor)))
		}
		boostMxInst = inst
	})
	return boostMxInst
}
