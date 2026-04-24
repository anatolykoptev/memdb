// Package search — cosine reranking for search results.
// Port of Python's CosineLocalReranker from cosine_local.py.
package search

import (
	"math"
	"os"
	"sort"
	"time"
)

// d1ImportanceCap bounds the importance multiplier so that a runaway
// access_count (e.g. 10k+) cannot dominate the final score. 1 + ln(1+148) ≈ 5,
// so 148 hits already saturate — beyond that the multiplier is flat.
const d1ImportanceCap = 5.0

// d1ImportanceEnabled reports whether the D1 combined-formula branch is
// active. Read on every call (not at init) so tests can flip the env var
// with t.Setenv without package-level var gymnastics.
//
// Default FALSE: ships the migration + access_count plumbing safely, the
// ranking formula itself is opt-in via MEMDB_D1_IMPORTANCE=true for A/B
// rollout.
func d1ImportanceEnabled() bool {
	return os.Getenv("MEMDB_D1_IMPORTANCE") == "true"
}

// hierarchyBoostEnabled reports whether the D3 hierarchy-tier boost is active.
// Gated by the same env that controls tree reorganization so enabling the
// boost without reorg is a no-op (no episodic/semantic rows exist yet).
// Read on every call — same rationale as d1ImportanceEnabled.
func hierarchyBoostEnabled() bool {
	return os.Getenv("MEMDB_REORG_HIERARCHY") == "true"
}

// hierarchyBoost returns a retrieval multiplier based on hierarchy_level.
// Semantic and episodic memories represent compressed, LLM-curated insight
// and outrank raw memories for identical cosine scores. Defaults match the
// D3 plan (1.15 / 1.08 / 1.0) and stay below the D1 importance cap so the
// overall score remains bounded.
//
// Values are runtime-tunable via MEMDB_D1_BOOST_SEMANTIC and
// MEMDB_D1_BOOST_EPISODIC — see tuning.go. Raw / unknown / missing levels
// always return 1.0.
func hierarchyBoost(meta map[string]any) float64 {
	lvl, _ := meta["hierarchy_level"].(string)
	switch lvl {
	case "semantic":
		return d1BoostSemantic()
	case "episodic":
		return d1BoostEpisodic()
	default:
		return 1.0
	}
}

// importanceMultiplier returns 1 + log(1 + access_count), capped at
// d1ImportanceCap (5.0). access_count is read from metadata as float64
// (FormatMemoryItem normalizes it). Missing / negative values clamp to 0,
// giving a multiplier of 1.0 (i.e. no importance boost).
func importanceMultiplier(meta map[string]any) float64 {
	c, _ := meta["access_count"].(float64)
	if c < 0 {
		c = 0
	}
	m := 1.0 + math.Log(1.0+c)
	if m > d1ImportanceCap {
		return d1ImportanceCap
	}
	return m
}

// ReRankByCosine re-scores items using cosine similarity between queryVec
// and each item's stored embedding. Items without embeddings keep their
// original score.
//
// This replaces the compressed PolarDB scores (0.88-0.92 range) with direct
// cosine similarity (0.2-0.95 range), giving much better discrimination
// for relativity threshold filtering.
func ReRankByCosine(queryVec []float32, items []map[string]any, embeddingsByID map[string][]float32) []map[string]any {
	if len(queryVec) == 0 || len(items) == 0 {
		return items
	}

	for _, item := range items {
		id, _ := item["id"].(string)
		emb := embeddingsByID[id]
		if len(emb) == 0 {
			continue
		}

		// Normalize to [0,1] range matching PolarDB's score: (raw_cosine + 1) / 2
		cosineSim := (float64(CosineSimilarity(queryVec, emb)) + 1.0) / 2.0
		if meta, ok := item["metadata"].(map[string]any); ok {
			meta["relativity"] = cosineSim
		}
	}

	// Sort by new score descending
	sort.SliceStable(items, func(i, j int) bool {
		scoreI := getRelativity(items[i])
		scoreJ := getRelativity(items[j])
		return scoreI > scoreJ
	})

	return items
}

// getRelativity extracts metadata.relativity from a formatted memory item.
func getRelativity(item map[string]any) float64 {
	meta, _ := item["metadata"].(map[string]any)
	if meta == nil {
		return 0
	}
	score, _ := meta["relativity"].(float64)
	return score
}

// ApplyTemporalDecay applies time-based recency scoring using a weighted combination:
//
//	final_score = SemanticWeight * cosine_score + RecencyWeight * recency_score
//	recency_score = exp(-alpha * days_since_last_access)  clamped to [0,1]
//
// Timestamp priority (matches LangChain/MemOS best practice):
//  1. last_accessed_at — reflects actual usage recency
//  2. updated_at — reflects content freshness
//  3. created_at — fallback
//
// Memories older than MaxDecayAgeDays get recency_score=0 but keep their
// semantic score (they are not removed, just ranked lower). This matches
// MemOS max_memory_age_days behavior.
//
// WorkingMemory items are exempt — session context should not decay.
// Items without any parseable timestamp keep their original score unchanged.
func ApplyTemporalDecay(items []map[string]any, now time.Time, alpha float64) []map[string]any {
	if alpha <= 0 || len(items) == 0 {
		return items
	}
	for _, item := range items {
		applyDecayToItem(item, now, alpha)
	}
	return items
}

// applyDecayToItem applies temporal decay to a single item's metadata in place.
//
// Two combiner formulas, gated by MEMDB_D1_IMPORTANCE:
//
//  1. Default (env unset / false) — legacy weighted sum:
//     final = DecaySemanticWeight*cosine + DecayRecencyWeight*recency
//
//  2. D1 combined (env = "true") — multiplicative with importance boost:
//     final = cosine * recency * (1 + log(1+access_count))   capped at 1.0
//     where recency = exp(-alpha * days) so alpha=ln(2)/180 gives a 180d
//     half-life. The importance multiplier is bounded at d1ImportanceCap
//     to keep runaway access_count from dominating the ranking.
//
// WorkingMemory is always exempt (session context — recency is its purpose).
// Items beyond MaxDecayAgeDays in the legacy branch keep the semantic floor;
// in D1 they get recency=0 which zeroes `final` (semantic cannot rescue
// very old memories — consistent with the D1 design goal of decaying out
// stale content).
func applyDecayToItem(item map[string]any, now time.Time, alpha float64) {
	meta, ok := item["metadata"].(map[string]any)
	if !ok || meta == nil {
		return
	}
	// WorkingMemory is session context — recency is already its purpose.
	if mt, _ := meta["memory_type"].(string); mt == "WorkingMemory" {
		return
	}

	refTime, ok := resolveRefTimestamp(meta)
	if !ok {
		return // no timestamp — leave score unchanged
	}

	days := now.Sub(refTime).Hours() / 24.0
	if days < 0 {
		days = 0
	}

	// Memories beyond MaxDecayAgeDays get recency=0 (semantic still counts in legacy mode).
	var recency float64
	if days <= MaxDecayAgeDays {
		recency = math.Exp(-alpha * days)
	}

	cosine, hasCosine := meta["relativity"].(float64)
	if !hasCosine {
		return
	}

	if d1ImportanceEnabled() {
		// D1 combined formula: cosine * decay * importance * hierarchy, capped at 1.0.
		// The hierarchy factor (D3) is 1.0 unless MEMDB_REORG_HIERARCHY=true AND
		// the item carries an episodic/semantic hierarchy_level. Applied inside
		// the D1 branch because D3 builds on D1's infrastructure — enabling D3
		// without D1 would mean the boost never runs, which is fine.
		imp := importanceMultiplier(meta)
		hier := 1.0
		if hierarchyBoostEnabled() {
			hier = hierarchyBoost(meta)
		}
		combined := cosine * recency * imp * hier
		if combined > 1.0 {
			combined = 1.0
		}
		meta["relativity"] = combined
		return
	}

	// Legacy weighted combination (default, matches MemOS/mem0-style patterns).
	meta["relativity"] = DecaySemanticWeight*cosine + DecayRecencyWeight*recency
}

// resolveRefTimestamp returns the most relevant timestamp from metadata.
// Priority: last_accessed_at > updated_at > created_at.
func resolveRefTimestamp(meta map[string]any) (time.Time, bool) {
	for _, field := range []string{"last_accessed_at", "updated_at", "created_at"} {
		if t, ok := parseTimestamp(meta, field); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseTimestamp extracts and parses a named timestamp field from metadata.
// Supports ISO 8601 formats stored as string.
func parseTimestamp(meta map[string]any, field string) (time.Time, bool) {
	raw, _ := meta[field].(string)
	if raw == "" {
		return time.Time{}, false
	}
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
