// Package handlers — atomic_semantic_dedup.go: pre-persist semantic
// dedupe for atomic facts.
//
// Why: even after the per-cube serial-batching fix (which closes the
// content_hash race), Gemini Flash still emits paraphrase variants of the
// same event across the four ingest passes (perspective × reverse-role).
// Example from v5b conv-26: 5 distinct facts about "Caroline LGBTQ
// conference", text differing only in adverb choice and clause order. Each
// occupies a top-k retrieval slot, dilutes the relevant-info ratio, and
// inflates index size for no recall gain.
//
// Pattern source: Zep / Graphiti's edge-dedup pass (arXiv 2501.13956) — same
// concept applied to flat atomic facts. After embedding (we already have the
// vector), kNN against the cube's existing atomic facts; if cosine ≥ a tight
// threshold, drop the new fact rather than persist a near-duplicate.
//
// Threshold rationale (atomicSemanticDedupThreshold = 0.92):
//   - 0.97 (existing nearDuplicateThreshold) is a CONVERSATION-LEVEL gate
//     that already runs in fetchFineCandidates — too loose for paraphrase
//     dedupe at the per-fact level.
//   - 0.85–0.90 catches even loosely-related facts ("had pets", "owns
//     animals") which aren't actually duplicates — false-drop risk.
//   - 0.92 is Zep's documented cutoff and matches the empirical "same event,
//     different wording" cluster on multilingual-e5-large embeddings tested
//     on the LoCoMo conv-26 corpus during the 2026-05-01 sprint.
//
// Trade-off: dedupe is per-FACT (one DB query per new fact) so cost grows
// linearly with extraction volume. For ~5 facts/extraction this is ~5 extra
// pgvector kNN lookups, each O(log N) on the HNSW halfvec index — negligible
// vs the ~4.5s LLM extract cost. Per-fact (not per-batch) so we can also
// dedupe within the same batch (later facts can hit earlier facts already
// committed in this loop).
package handlers

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// atomicSemanticDedupDefaultThreshold — cosine-similarity floor at which a
// new atomic fact is treated as a duplicate of an existing one in the same
// cube. Operator override via MEMDB_ATOMIC_DEDUP_THRESHOLD.
//
// 2026-05-01 evolution:
//   - 0.92 (Zep recommendation, initial): produced 42% drop rate on conv-26 —
//     too aggressive for multilingual-e5-large. False-drops included
//     "Caroline went hiking" vs "Caroline likes outdoor activities" → cat2
//     multi-hop lost connector facts (judge regression -10pp).
//   - 0.95 (current): tighter cluster, only true paraphrases dropped.
//     Expected ~15-25% drop rate — keeps cat3/cat5 facts that share topic
//     but carry distinct details.
const atomicSemanticDedupDefaultThreshold = 0.95

// atomicSemanticDedupThreshold returns the live cutoff. Reads
// MEMDB_ATOMIC_DEDUP_THRESHOLD on every call (cheap; allows hot-tuning during
// A/B sweeps without container restart).
func atomicSemanticDedupThreshold() float64 {
	v, ok := os.LookupEnv("MEMDB_ATOMIC_DEDUP_THRESHOLD")
	if !ok || v == "" {
		return atomicSemanticDedupDefaultThreshold
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || f > 1 {
		return atomicSemanticDedupDefaultThreshold
	}
	return f
}

// atomicDedupEnabled gates the dedupe path so operators can A/B vs the
// no-dedupe baseline by setting MEMDB_ATOMIC_SEMANTIC_DEDUP=0. Default ON —
// dedupe is the documented safer-of-two-evils choice (false drops are
// recoverable via re-extraction; near-duplicate spam is permanent storage
// pollution).
func atomicDedupEnabled() bool {
	return envBoolDefault("MEMDB_ATOMIC_SEMANTIC_DEDUP", true)
}

// envBoolDefault parses a "true"/"1" env value, returning fallback on absent
// or unparsable input. Lives here (not envcfg) because this is the only
// caller and the helper would be lonely in a shared package.
func envBoolDefault(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

// dedupAtomicFactsAgainstCube returns the subset of `embedded` that should
// actually be persisted — facts whose embeddings have NO existing atomic-fact
// neighbour in the cube above the similarity threshold are kept; near-
// duplicates are dropped.
//
// Returns: (kept, droppedCount). The dropped slice itself is not surfaced —
// the only consumer is the metric counter and a debug log line for the
// operator.
//
// On error (DB unavailable, cancellation), the function fails OPEN: returns
// the full input and zero dropped. Better to admit a duplicate than to
// silently lose new facts because the dedupe lookup happened to flake.
func (h *Handler) dedupAtomicFactsAgainstCube(
	ctx context.Context,
	cubeID string,
	embedded []embeddedFact,
) ([]embeddedFact, int) {
	if !atomicDedupEnabled() || h == nil || h.postgres == nil || len(embedded) == 0 {
		return embedded, 0
	}
	threshold := atomicSemanticDedupThreshold()

	// 2026-05-01: parallelize per-fact kNN probes. Serial loop on a 5-fact
	// batch added 1-2s to the apply stage (each probe ~200-400ms with HNSW).
	// pgvector kNN is read-only and concurrent-safe; pgxpool already gives us
	// up to PoolSize=10 connections. Fan out probes via a worker pool sized
	// to the smaller of len(embedded) and a sane cap so we don't starve other
	// add workers' pool slots.
	type probeResult struct {
		hit *db.SimilarMemoryHit
		err error
	}
	const maxConcurrent = 8
	concurrent := len(embedded)
	if concurrent > maxConcurrent {
		concurrent = maxConcurrent
	}
	results := make([]probeResult, len(embedded))
	sem := make(chan struct{}, concurrent)
	var wg sync.WaitGroup
	for i := range embedded {
		ef := embedded[i]
		if len(ef.embedding) == 0 || ef.fact.Memory == "" {
			// No embedding → no probe; treated as kept downstream.
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, vec []float32) {
			defer wg.Done()
			defer func() { <-sem }()
			hit, err := h.postgres.NearestAtomicFact(ctx, cubeID, vec)
			results[idx] = probeResult{hit: hit, err: err}
		}(i, ef.embedding)
	}
	wg.Wait()

	kept := make([]embeddedFact, 0, len(embedded))
	dropped := 0
	for i := range embedded {
		ef := embedded[i]
		if len(ef.embedding) == 0 || ef.fact.Memory == "" {
			kept = append(kept, ef)
			continue
		}
		res := results[i]
		if res.err != nil {
			h.logger.Debug("atomic semantic dedup: kNN probe failed",
				slog.String("cube_id", cubeID), slog.Any("error", res.err))
			recordAtomicDedupOutcome(ctx, "probe_error")
			kept = append(kept, ef)
			continue
		}
		if res.hit == nil || res.hit.Similarity < threshold {
			recordAtomicDedupOutcome(ctx, "kept")
			kept = append(kept, ef)
			continue
		}
		dropped++
		recordAtomicDedupOutcome(ctx, "dropped")
		h.logger.Debug("atomic semantic dedup: dropped near-duplicate",
			slog.String("cube_id", cubeID),
			slog.Float64("similarity", res.hit.Similarity),
			slog.String("new_text", truncateLog(ef.fact.Memory, 80)),
			slog.String("existing_text", truncateLog(res.hit.Memory, 80)),
			slog.String("existing_id", res.hit.ID),
		)
	}
	return kept, dropped
}

// truncateLog clips long fact text in debug log lines so a 12-fact batch
// against a multi-paragraph cube doesn't blow up the log output. Mirrors the
// truncateForLog helper in atomic_extractor_salvage_test but lives here to
// avoid pulling test code into the production binary.
func truncateLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Outcome label values for memdb.atomic.semantic_dedup_total:
//
//	kept         — fact's nearest neighbour is below threshold (or no neighbour); persisted
//	dropped      — fact's nearest neighbour is ≥ threshold; treated as paraphrase, dropped
//	probe_error  — kNN query failed; failed open (kept), counter is for ops alerting
var (
	atomicDedupCounterOnce sync.Once
	atomicDedupCounter     metric.Int64Counter
)

func recordAtomicDedupOutcome(ctx context.Context, outcome string) {
	atomicDedupCounterOnce.Do(func() {
		m := otel.Meter("memdb-go/handlers")
		c, _ := m.Int64Counter("memdb.atomic.semantic_dedup_total",
			metric.WithDescription("Outcomes of pre-persist atomic-fact semantic dedup (kept|dropped|probe_error). Drop-rate = dropped/(kept+dropped)."),
		)
		atomicDedupCounter = c
	})
	if atomicDedupCounter == nil {
		return
	}
	atomicDedupCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}
