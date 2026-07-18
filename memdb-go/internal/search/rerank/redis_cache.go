// Package rerank — redis_cache.go: Redis-backed implementation of the
// go-kit/rerank.Cache interface, mirroring internal/embedder/redis_cache.go.
//
// Why: cross-encoder rerank (bge-reranker-v2-m3 + gte-multi-rerank) is the
// most expensive search-side LLM-adjacent call (~200-500 ms per batch on
// Neoverse-N1). Same query repeated across query workers — D4/D7 rewrite
// variants of one user query, plus the original — fans out N reranks of
// nearly-identical (model, query, doc.text) tuples. The go-kit/rerank
// package ships full cache infra (Cache interface + WithCache + tryCache
// fullBatchGet) but memdb-go has not yet wired a backend. This file ships
// the Redis backend; server_init_search.go injects it next to WithCircuit.
//
// Pairs with the embed cache (separate Redis namespace prefix). Same
// cache.Client pool — no extra Redis connections.
package rerank

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/cache"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// rerankCachePrefix namespaces our Redis keys so they don't collide with the
// embed cache (embed:v1:) or response cache.
const rerankCachePrefix = "rerank:v1:"

// DefaultRerankCacheTTL — 7 days. Cross-encoder scores for a fixed (model,
// query, doc) triple are deterministic; long TTL is safe.
const DefaultRerankCacheTTL = 7 * 24 * time.Hour

// RedisRerankCache implements go-kit/rerank.Cache using the existing
// memdb-go Redis client. Mirrors RedisEmbedCache structure exactly so any
// future fix benefits both layers.
type RedisRerankCache struct {
	client *cache.Client
	ttl    time.Duration
	logger *slog.Logger
}

// NewRedisRerankCache constructs a cache. Returns nil if client is nil so
// callers can pass through `WithCache(NewRedisRerankCache(c, …))` and rely
// on go-kit/rerank's nil-Cache short-circuit.
func NewRedisRerankCache(client *cache.Client, ttl time.Duration, logger *slog.Logger) *RedisRerankCache {
	if client == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultRerankCacheTTL
	}
	return &RedisRerankCache{client: client, ttl: ttl, logger: logger}
}

// Get implements go-kit/rerank.Cache. Returns (0, false) on miss / error /
// disabled. Errors logged at warn — silent failure inflates rerank load
// without operator visibility.
func (r *RedisRerankCache) Get(ctx context.Context, key string) (float32, bool) {
	if r == nil || r.client == nil {
		return 0, false
	}
	raw, err := r.client.Get(ctx, rerankCachePrefix+key)
	if err != nil {
		// Caller-cancelled ctx is the dominant "error" under parallel
		// query workers — one slow rerank trips the per-query deadline
		// and cancels the rest mid-Redis-Get. Not a cache fault; classify
		// separately so the get_error rate reflects real Redis problems.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			recordRerankCacheOutcome(ctx, "ctx_canceled")
			return 0, false
		}
		recordRerankCacheOutcome(ctx, "get_error")
		r.logger.Warn("rerank cache get failed", slog.String("key", key), slog.Any("error", err))
		return 0, false
	}
	if raw == nil {
		recordRerankCacheOutcome(ctx, "miss")
		return 0, false
	}
	if len(raw) != 4 {
		recordRerankCacheOutcome(ctx, "decode_error")
		r.logger.Warn("rerank cache decode failed: expected 4-byte float32",
			slog.String("key", key), slog.Int("got_bytes", len(raw)))
		return 0, false
	}
	score := math.Float32frombits(binary.LittleEndian.Uint32(raw))
	recordRerankCacheOutcome(ctx, "hit")
	return score, true
}

// Set implements go-kit/rerank.Cache. Idempotent. Errors logged but never
// returned (Cache interface signature has no error — by design).
func (r *RedisRerankCache) Set(ctx context.Context, key string, score float32) {
	if r == nil || r.client == nil {
		return
	}
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, math.Float32bits(score))
	if err := r.client.Set(ctx, rerankCachePrefix+key, buf, r.ttl); err != nil {
		recordRerankCacheOutcome(ctx, "set_error")
		r.logger.Warn("rerank cache set failed",
			slog.String("key", key), slog.Any("error", err))
		return
	}
	recordRerankCacheOutcome(ctx, "set")
}

// recordRerankCacheOutcome bumps memdb.rerank.cache_total{outcome=…}.
// Outcome label values: hit | miss | set | get_error | set_error |
// decode_error.
var (
	rerankCacheCounterOnce sync.Once
	rerankCacheCounter     metric.Int64Counter
)

func recordRerankCacheOutcome(ctx context.Context, outcome string) {
	rerankCacheCounterOnce.Do(func() {
		m := otel.Meter("memdb-go/rerank")
		c, _ := m.Int64Counter("memdb.rerank.cache_total",
			metric.WithDescription("Outcomes of the Redis-backed rerank score cache (hit|miss|set|get_error|set_error|decode_error). Hit-rate = hit/(hit+miss)."),
		)
		rerankCacheCounter = c
	})
	if rerankCacheCounter == nil {
		return
	}
	rerankCacheCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}
