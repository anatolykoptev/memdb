package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// Cache abstracts a (text → vector) lookup table. go-kit/embed ships NO concrete
// implementation — callers wire LRU/Redis/sync.Map per their runtime.
// Implementations MUST be safe for concurrent reads and writes.
//
// TTL semantics, eviction policy, and persistence are caller concerns.
// Cache key invalidation on model/dim/prefix change is automatic
// (key includes all parameters that affect output vector).
//
// Trade-offs:
//   - On partial-miss, ALL N vectors are re-Set after the backend call (not
//     only the missing ones). For Redis-backed caches with non-trivial Set
//     cost, implementations may dedupe internally. In-process LRU/sync.Map
//     caches: noop (Set is O(1) and idempotent).
//
// Future-proofing — these vector-affecting fields are NOT YET in cacheKey
// because they are static or single-valued today; once they become per-call
// settable, cacheKey will be extended:
//   - Voyage input_type ("document" vs "query") — hardcoded "query" today
//   - Ollama normalize_l2 toggle — applied unconditionally today
//
// Callers persisting a cache across Client lifecycles SHOULD include their own
// config-hash prefix on keys to avoid cross-config pollution.
type Cache interface {
	// Get returns the cached embedding for the given key. ok=false if not cached.
	// Implementations must NOT panic on ctx cancellation; return ok=false instead.
	Get(ctx context.Context, key string) (vector []float32, ok bool)
	// Set stores the embedding for the given key. Idempotent. Implementations
	// may TTL or evict per their policy.
	Set(ctx context.Context, key string, vector []float32)
}

// WithCache wires a Cache. When set, every (model, dim, docPrefix, queryPrefix,
// text) tuple is looked up before backend Embed call. Full-batch hit
// short-circuits the backend entirely. Partial misses fall through to the
// backend for the full batch (no cherry-pick; keeps API symmetric across all
// backends). A nil Cache is ignored (caching stays disabled).
func WithCache(c Cache) Opt {
	return func(cfg *cfgInternal) {
		if c != nil {
			cfg.cache = c
		}
	}
}

// cacheKey computes the deterministic key for a (model, dim, docPrefix,
// queryPrefix, text) tuple.
// Format: sha256(model NUL itoa(dim) NUL docPrefix NUL queryPrefix NUL text).
// Hex-encoded 64-char string.
//
// Why all 5 fields:
//
//	model       — different models produce different vector spaces
//	dim         — Matryoshka truncation (E4) changes output when dim < full
//	docPrefix   — e5-family "passage: " prepends to text before embedding
//	queryPrefix — "query: " for retrieval asymmetry (stored vs queried)
//	text        — the input itself
//
// SHA-256 (not MD5): FIPS-compatible, collision-resistant for arbitrary inputs.
// NUL separator: prevents collision when a field value contains another's boundary
// (e.g. model="ab\x00" text="" vs model="ab" text="\x00").
func cacheKey(model string, dim int, docPrefix, queryPrefix, text string) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(itoa(dim)))
	h.Write([]byte{0})
	h.Write([]byte(docPrefix))
	h.Write([]byte{0})
	h.Write([]byte(queryPrefix))
	h.Write([]byte{0})
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

// tryCacheFullBatchGet returns a slice of vectors (ordered matching texts[]) if
// ALL texts have cached entries. Returns nil on any single miss — the caller
// must fall through to the backend for the full batch.
func tryCacheFullBatchGet(ctx context.Context, cache Cache, model string, dim int, docPrefix, queryPrefix string, texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if ctx.Err() != nil {
			return nil // ctx cancelled — abort cache lookup
		}
		vec, ok := cache.Get(ctx, cacheKey(model, dim, docPrefix, queryPrefix, t))
		if !ok {
			return nil // partial miss — fall through to backend
		}
		out[i] = vec
	}
	return out
}
