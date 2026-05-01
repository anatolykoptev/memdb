// Package embedder — redis_cache.go: Redis-backed implementation of the
// go-kit/embed.Cache interface, used to short-circuit duplicate embed-server
// calls.
//
// Why: embed-server (multilingual-e5-large) saw 11 900 cache misses against
// 16 643 hits on its in-process LRU (58% hit rate) during the LoCoMo sprint.
// Each miss = one ONNX inference (~50–100 ms on Neoverse-N1 with cold ARM
// cache, much worse under concurrent load when BATCH_MAX > 8 thrashes the L2).
// Hoisting the cache one layer up — into memdb-go — saves the HTTP round trip
// AND the embed-server's own cache lookup cost, AND survives embed-server
// restart since Redis is persistent.
//
// Mirrors the go-code optimisation pattern (commit 3f4001d, "cache symbol
// entries via go-kit cache.GetIfValid — −80% embed-server traffic"). The
// memory note for embed-server explicitly identifies this lever as the
// production-grade fix instead of bumping memory limits or BATCH_MAX past the
// hardware ceiling (>8 = ARM cache thrash on Neoverse-N1).
//
// Cache key derivation lives in go-kit/embed (cacheKey() — sha256 over
// model||dim||docPrefix||queryPrefix||text). We only own the persistence
// layer.
package embedder

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

// embedCachePrefix namespaces our Redis keys so they don't collide with the
// search-result cache or ad-hoc Redis usage by other services on the same DB.
const embedCachePrefix = "embed:v1:"

// DefaultEmbedCacheTTL is the default TTL for cached embedding vectors. Long
// (7 days) because (model, text) → vector is deterministic — the only reason
// to expire is to bound key-space growth. Override via NewRedisEmbedCache's
// ttl arg or the MEMDB_EMBED_CACHE_TTL env (wired in factory.go).
const DefaultEmbedCacheTTL = 7 * 24 * time.Hour

// RedisEmbedCache implements go-kit/embed.Cache using the existing memdb-go
// Redis client. The struct is intentionally thin — encoding/decoding is the
// only responsibility beyond delegating to cache.Client.
//
// Concurrency: cache.Client wraps go-redis/v9 which is safe for concurrent
// Get/Set. We add no internal locking.
type RedisEmbedCache struct {
	client *cache.Client
	ttl    time.Duration
	logger *slog.Logger
}

// NewRedisEmbedCache constructs a cache backed by the supplied Redis client.
// Returns nil if client is nil — caller must check (factory does).
//
// ttl=0 falls back to DefaultEmbedCacheTTL. Pass an explicit TTL only when an
// experiment demands faster invalidation (e.g. model A/B with shared keys —
// but cache key already includes model so this is unusual).
func NewRedisEmbedCache(client *cache.Client, ttl time.Duration, logger *slog.Logger) *RedisEmbedCache {
	if client == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultEmbedCacheTTL
	}
	return &RedisEmbedCache{client: client, ttl: ttl, logger: logger}
}

// Get implements go-kit/embed.Cache. Returns (nil, false) on any error or
// miss — the go-kit caller treats this as "fall through to backend" without
// surfacing the error. We log infrastructure errors (Redis unreachable,
// decode bug) at warn level since silent failures inflate embed-server load
// without operator visibility.
func (r *RedisEmbedCache) Get(ctx context.Context, key string) ([]float32, bool) {
	if r == nil || r.client == nil {
		return nil, false
	}
	raw, err := r.client.Get(ctx, embedCachePrefix+key)
	if err != nil {
		recordEmbedCacheOutcome(ctx, "get_error")
		r.logger.Warn("embed cache get failed", slog.String("key", key), slog.Any("error", err))
		return nil, false
	}
	if raw == nil {
		recordEmbedCacheOutcome(ctx, "miss")
		return nil, false
	}
	vec, err := decodeFloat32Vector(raw)
	if err != nil {
		recordEmbedCacheOutcome(ctx, "decode_error")
		r.logger.Warn("embed cache decode failed",
			slog.String("key", key), slog.Int("bytes", len(raw)), slog.Any("error", err))
		return nil, false
	}
	recordEmbedCacheOutcome(ctx, "hit")
	return vec, true
}

// Set implements go-kit/embed.Cache. Idempotent. Errors are logged but never
// returned (Cache interface signature has no error return — by design, a
// failing cache must not break embedding lookups).
func (r *RedisEmbedCache) Set(ctx context.Context, key string, vec []float32) {
	if r == nil || r.client == nil || len(vec) == 0 {
		return
	}
	encoded := encodeFloat32Vector(vec)
	if err := r.client.Set(ctx, embedCachePrefix+key, encoded, r.ttl); err != nil {
		recordEmbedCacheOutcome(ctx, "set_error")
		r.logger.Warn("embed cache set failed",
			slog.String("key", key), slog.Int("bytes", len(encoded)), slog.Any("error", err))
		return
	}
	recordEmbedCacheOutcome(ctx, "set")
}

// encodeFloat32Vector serialises a vector as little-endian IEEE-754 float32
// bytes. 4 bytes per element, no header — the cache key already encodes the
// dimension via cacheKey(), so length-prefixing would be redundant.
//
// Endianness is fixed (little-endian) for cross-arch portability — Redis may
// be on x86 in future even if memdb-go runs on ARM today.
func encodeFloat32Vector(vec []float32) []byte {
	out := make([]byte, 4*len(vec))
	for i, f := range vec {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(f))
	}
	return out
}

// decodeFloat32Vector reverses encodeFloat32Vector. Returns an error on
// non-multiple-of-4 length (corruption sentinel — should never happen but
// guards against a downstream Redis client bug or value-type mismatch).
func decodeFloat32Vector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, errEmbedCacheCorrupt
	}
	n := len(b) / 4
	vec := make([]float32, n)
	for i := 0; i < n; i++ {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return vec, nil
}

var errEmbedCacheCorrupt = errors.New("embed cache: corrupt vector blob (length % 4 != 0)")

// embedCacheCounter outcomes:
//
//	hit          — Get succeeded and decoded a valid vector
//	miss         — Get returned no value (raw == nil)
//	set          — Set succeeded
//	get_error    — Redis Get returned a transport error
//	set_error    — Redis Set returned a transport error
//	decode_error — value present in Redis but couldn't be decoded
var (
	embedCacheCounterOnce sync.Once
	embedCacheCounter     metric.Int64Counter
)

func recordEmbedCacheOutcome(ctx context.Context, outcome string) {
	embedCacheCounterOnce.Do(func() {
		m := otel.Meter("memdb-go/embedder")
		c, _ := m.Int64Counter("memdb.embed.cache_total",
			metric.WithDescription("Outcomes of the Redis-backed embed cache (hit|miss|set|get_error|set_error|decode_error). Hit-rate = hit/(hit+miss)."),
		)
		embedCacheCounter = c
	})
	if embedCacheCounter == nil {
		return
	}
	embedCacheCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}
