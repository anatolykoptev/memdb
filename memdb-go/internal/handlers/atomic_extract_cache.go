// Package handlers — atomic_extract_cache.go: Redis-backed cache for the
// atomic-fact LLM extraction call.
//
// Why: ExtractAtomicFacts is the dominant ingest cost — observed 4.5s avg
// per call (Gemini Flash 3.1) against an embed call avg of 2.6s after the
// E3 embed cache landed. With 77 add calls in a LoCoMo full-conv ingest,
// extract dominates total wall time.
//
// What is cached: the (conversation, observation_date, candidate fingerprint,
// prompt body hash) → []AtomicFact tuple. The prompt body hash gates cache
// invalidation when the operator hot-reloads atomic-extractor.md (mtime
// pickup via skillkit) — old cached facts won't be served against a new
// prompt that asks for different output structure.
//
// What is NOT cached: NamedEntitiesInText, AttributedTo, EventDates, and
// LinkedMemoryIDs DO survive cache hits (they are part of the AtomicFact
// struct that gets gob-encoded). Only the LLM call is short-circuited.
//
// TTL: 24h default. Atomic facts are deterministic outputs of (input
// + prompt) — long TTL is safe. Operator can flush via Redis FLUSHDB on
// db 1 or wait for natural expiry. Override via MEMDB_ATOMIC_EXTRACT_CACHE_TTL.
package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/cache"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// atomicExtractCachePrefix namespaces Redis keys so they don't collide
	// with the embed cache (embed:v1:) or response cache.
	atomicExtractCachePrefix = "atomic-extract:v1:"
	// defaultAtomicExtractCacheTTL — 24h. Atomic extraction is deterministic
	// for a given (input, prompt) pair, so long TTL is safe. Tuned down via
	// env override if a prompt iteration cadence demands faster invalidation.
	defaultAtomicExtractCacheTTL = 24 * time.Hour
)

// atomicExtractCache wraps the existing cache.Client to serve as a one-call
// short-circuit in front of llm.ExtractAtomicFacts. Nil-safe: if client is
// nil, all operations are no-ops and the caller falls through to the LLM.
type atomicExtractCache struct {
	client *cache.Client
	ttl    time.Duration
	logger *slog.Logger
}

// newAtomicExtractCache constructs the cache. ttl=0 falls back to
// defaultAtomicExtractCacheTTL. Returns nil if client is nil.
func newAtomicExtractCache(client *cache.Client, ttl time.Duration, logger *slog.Logger) *atomicExtractCache {
	if client == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = defaultAtomicExtractCacheTTL
	}
	return &atomicExtractCache{client: client, ttl: ttl, logger: logger}
}

// Get returns the cached fact slice. (nil, false) on miss / error / disabled.
// Errors are logged at warn — silent miss > silent corrupted output.
func (a *atomicExtractCache) Get(ctx context.Context, key string) ([]llm.AtomicFact, bool) {
	if a == nil || a.client == nil {
		return nil, false
	}
	raw, err := a.client.Get(ctx, atomicExtractCachePrefix+key)
	if err != nil {
		recordAtomicCacheOutcome(ctx, "get_error")
		a.logger.Warn("atomic-extract cache get failed", slog.String("key", key), slog.Any("error", err))
		return nil, false
	}
	if raw == nil {
		recordAtomicCacheOutcome(ctx, "miss")
		return nil, false
	}
	var facts []llm.AtomicFact
	if err := json.Unmarshal(raw, &facts); err != nil {
		recordAtomicCacheOutcome(ctx, "decode_error")
		a.logger.Warn("atomic-extract cache decode failed",
			slog.String("key", key), slog.Int("bytes", len(raw)), slog.Any("error", err))
		return nil, false
	}
	recordAtomicCacheOutcome(ctx, "hit")
	return facts, true
}

// Set stores the fact slice. Errors are logged but never returned —
// downstream extract path should never fail because of cache write trouble.
func (a *atomicExtractCache) Set(ctx context.Context, key string, facts []llm.AtomicFact) {
	if a == nil || a.client == nil || len(facts) == 0 {
		return
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		recordAtomicCacheOutcome(ctx, "encode_error")
		a.logger.Warn("atomic-extract cache encode failed", slog.Any("error", err))
		return
	}
	if err := a.client.Set(ctx, atomicExtractCachePrefix+key, encoded, a.ttl); err != nil {
		recordAtomicCacheOutcome(ctx, "set_error")
		a.logger.Warn("atomic-extract cache set failed",
			slog.String("key", key), slog.Int("bytes", len(encoded)), slog.Any("error", err))
		return
	}
	recordAtomicCacheOutcome(ctx, "set")
}

// computeAtomicCacheKey builds the deterministic key for an extraction call.
// Inputs:
//
//	cubeID          — per-cube isolation
//	observationDate — temporal anchor (date-only YYYY-MM-DD; never includes time)
//	conversation    — formatted conversation text already passed to the LLM
//	candidates      — DELIBERATELY IGNORED in the key. See rationale below.
//	promptBody      — current atomic-extractor skill body (env override or
//	                  embedded default); when the prompt changes the cache is
//	                  implicitly invalidated
//
// 2026-05-01 evolution:
//
//   - v6 hashed candidate UUIDs → 1.3% hit rate (UUIDs flip every re-ingest
//     because cleanup + new write → fresh row IDs).
//   - v7 hashed candidate TEXT → 12% hit rate (text *content* drifts within
//     a per-cube serial pass: chunk N+1 sees facts written by chunks 1..N,
//     and parallel scheduling between cubes shifts the order non-
//     deterministically — so even byte-identical conversation produces a
//     different candidate set across runs).
//   - v8 (this commit): drop candidates from the key entirely.
//
// Trade-off: candidates feed the LLM's `linked_memory_ids` slot. A cache hit
// returns whatever linked_memory_ids the LLM produced FIRST time it saw the
// chunk. On re-ingest after cleanup those UUIDs are stale and the F12 linked-
// expand resolver will silently drop them at lookup time (no panic, no
// cascade). For the dominant cache-target workloads (idempotent re-ingest,
// reverse-role pass, accidental webhook double-fire) this is acceptable —
// linked_memory_ids is a soft enhancement, not a correctness requirement.
//
// Future work: if linked_memory_ids becomes load-bearing, switch from full-
// fact caching to a two-layer cache (fact text cached here + linked_ids
// recomputed at read time via embedding lookup).
func computeAtomicCacheKey(cubeID, observationDate, conversation string, candidates []llm.Candidate, promptBody string) string {
	_ = candidates // intentionally unused; see docblock for rationale

	h := sha256.New()
	h.Write([]byte(cubeID))
	h.Write([]byte{0})
	h.Write([]byte(observationDate))
	h.Write([]byte{0})
	// Prompt body hash gates cache invalidation on operator hot-reload of
	// atomic-extractor.md. Hashed separately to avoid bloating the outer
	// SHA input with a multi-KB blob — sha256(body) → 32 bytes.
	bodyHash := sha256.Sum256([]byte(promptBody))
	h.Write(bodyHash[:])
	h.Write([]byte{0})
	h.Write([]byte(conversation))
	return hex.EncodeToString(h.Sum(nil))
}

// recordAtomicCacheOutcome bumps memdb.atomic.extract_cache_total{outcome=…}.
// Outcome values: hit | miss | set | get_error | set_error | decode_error |
// encode_error.
var (
	atomicExtractCounterOnce sync.Once
	atomicExtractCounter     metric.Int64Counter
)

func recordAtomicCacheOutcome(ctx context.Context, outcome string) {
	atomicExtractCounterOnce.Do(func() {
		m := otel.Meter("memdb-go/handlers")
		c, _ := m.Int64Counter("memdb.atomic.extract_cache_total",
			metric.WithDescription("Outcomes of the Redis-backed atomic-extract LLM-call cache (hit|miss|set|get_error|set_error|encode_error|decode_error). Hit-rate = hit/(hit+miss)."),
		)
		atomicExtractCounter = c
	})
	if atomicExtractCounter == nil {
		return
	}
	atomicExtractCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

