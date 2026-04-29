package search

// rewrite_cache.go — in-memory LRU cache for D4 query-rewrite and D7 CoT
// decompose LLM results.
//
// Motivation: direct Gemini Flash-lite latency is 1.3-1.4 s/call. D4 avg is
// 2.0-2.8 s in prod, and D7 similar. On LoCoMo bench (50 QA × 6 variants =
// 300 calls), the same 50 queries are repeated 5× after the first variant —
// expected cache hit rate ≥ 50% on bench, 5-15% on prod traffic.
//
// Design:
//   - Key: sha256(query + "|" + modelName + "|" + cacheVersion)[:8 bytes]
//     encoded as 16-char hex. 2^64 keyspace — collision probability negligible
//     at any realistic corpus size.
//   - Value: the raw string the LLM returned (no transform on write/read).
//   - Capacity: MEMDB_REWRITE_CACHE_SIZE (default 10000). 0 = disabled (every
//     call goes to LLM as normal — no silent drop of D4/D7 functionality).
//   - TTL: none by default (rewrites are deterministic at temperature 0).
//     Optional MEMDB_REWRITE_CACHE_TTL_S > 0 adds expiry.
//   - Thread-safe: sync.RWMutex + ring-buffer eviction (avoids importing
//     hashicorp/golang-lru — keeps go.mod clean).
//
// Pre-register metrics at zero so Prometheus sees the series from container
// start, matching the pattern in metrics.go.

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	rewriteCacheVersion = "v1"
	rewriteCacheDefault = 10000
)

// rewriteCache is a bounded, optionally-TTL-guarded string→string cache.
// Safe for concurrent use. A zero-value rewriteCache behaves as "disabled":
// every Get returns ("", false) and every Set is a no-op.
type rewriteCache struct {
	mu       sync.RWMutex
	cap      int
	ttl      time.Duration // 0 = no expiry
	entries  map[string]*rewriteCacheEntry
	evictRing []string // insertion-order ring for eviction; len == cap
	ringHead  int
}

type rewriteCacheEntry struct {
	value   string
	expires time.Time // zero → no expiry
}

// rewriteCacheKey returns the 16-char hex cache key for (query, modelName).
// The first 8 bytes of sha256 provide 64-bit key space — ample for any
// realistic query corpus.
func rewriteCacheKey(query, modelName string) string {
	h := sha256.Sum256([]byte(query + "|" + modelName + "|" + rewriteCacheVersion))
	key64 := binary.BigEndian.Uint64(h[:8])
	return fmt.Sprintf("%016x", key64)
}

// newRewriteCache reads MEMDB_REWRITE_CACHE_SIZE and MEMDB_REWRITE_CACHE_TTL_S
// from the environment and returns a ready cache. Returns nil when size=0
// (disabled — callers must handle nil and bypass the cache layer entirely,
// NOT drop D4/D7 logic).
func newRewriteCache() *rewriteCache {
	size := rewriteCacheDefault
	if s := os.Getenv("MEMDB_REWRITE_CACHE_SIZE"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			size = v
		}
	}
	if size <= 0 {
		return nil // disabled
	}

	var ttl time.Duration
	if s := os.Getenv("MEMDB_REWRITE_CACHE_TTL_S"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			ttl = time.Duration(v) * time.Second
		}
	}

	return &rewriteCache{
		cap:       size,
		ttl:       ttl,
		entries:   make(map[string]*rewriteCacheEntry, size),
		evictRing: make([]string, size),
	}
}

// Get retrieves a cached rewrite result. Returns ("", false) on miss or expiry.
func (c *rewriteCache) Get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if !e.expires.IsZero() && time.Now().After(e.expires) {
		// expired — treat as miss (lazy eviction at next Set)
		return "", false
	}
	return e.value, true
}

// Set stores a rewrite result. Evicts the oldest entry when at capacity.
// No-op on nil cache.
func (c *rewriteCache) Set(key, value string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[key]; exists {
		// Update in-place (no eviction needed — slot already counted).
		e := c.entries[key]
		e.value = value
		if c.ttl > 0 {
			e.expires = time.Now().Add(c.ttl)
		}
		return
	}

	// Evict oldest ring entry if at capacity.
	if len(c.entries) >= c.cap {
		oldest := c.evictRing[c.ringHead]
		delete(c.entries, oldest)
	}

	var exp time.Time
	if c.ttl > 0 {
		exp = time.Now().Add(c.ttl)
	}
	c.entries[key] = &rewriteCacheEntry{value: value, expires: exp}
	c.evictRing[c.ringHead] = key
	c.ringHead = (c.ringHead + 1) % c.cap
}

// global singleton — initialised once in init() so it is ready before the
// first search request.
var (
	globalRewriteCache     *rewriteCache
	globalRewriteCacheOnce sync.Once
)

func getRewriteCache() *rewriteCache {
	globalRewriteCacheOnce.Do(func() {
		globalRewriteCache = newRewriteCache()
	})
	return globalRewriteCache
}
