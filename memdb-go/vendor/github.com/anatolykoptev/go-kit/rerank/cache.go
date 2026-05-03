package rerank

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// cacheKeyVersion is a sentinel mixed into every cacheKey to invalidate
// previously persisted entries when the key shape changes.
//
//	v1 — model, serverNormalize, queryInstr, docInstr, query, docText
//	v2 — adds maxCharsPerDoc + maxTokensPerDoc to invalidate when truncation
//	     caps change (server truncates before scoring; same docText returns
//	     different scores under different caps).
const cacheKeyVersion = "v2"

// Cache abstracts a (model, query, doc.Text) → score lookup table. go-kit/rerank
// ships NO concrete implementation — callers wire Redis, LRU, sync.Map, or any
// other store per their runtime. Implementations MUST be safe for concurrent
// reads and writes.
//
// TTL semantics, eviction policy, and persistence are caller concerns.
// Cache key invalidation on model change is automatic: cacheKey includes the
// model name, so switching WithModel auto-invalidates without explicit purge.
type Cache interface {
	// Get returns the cached score for the given key and ok=true if present.
	// Returns ok=false on cache miss or if ctx is cancelled.
	// Implementations MUST NOT panic; return ok=false on any internal error.
	Get(ctx context.Context, key string) (score float32, ok bool)
	// Set stores the score under the given key. Idempotent: no error is surfaced
	// (cache writes are best-effort). Implementations may apply TTL or eviction
	// per their policy.
	Set(ctx context.Context, key string, score float32)
}

// WithCache wires a Cache to the client. When set, every (model, query, doc.Text)
// triple is looked up before the HTTP call. A full-batch cache hit (all docs
// present in the cache) short-circuits the network call entirely.
//
// Partial hits fall through to HTTP for the full batch — per-doc selective
// requests are not implemented (keeps the server protocol simple).
//
// A nil Cache is ignored (caching stays disabled).
func WithCache(c Cache) Opt {
	return func(cfg *cfgInternal) {
		if c != nil {
			cfg.cache = c
		}
	}
}

// cacheKey computes the deterministic key for a (model, query, doc.Text) triple
// plus all server-side knobs that change the returned score.
// Format: sha256(version NUL model NUL serverNormalize NUL queryInstr NUL docInstr
//                NUL query NUL docText NUL maxCharsPerDoc NUL maxTokensPerDoc).
// All inputs that change the upstream rerank response MUST be in the key —
// otherwise a cached score from one config gets returned under another, silently.
//
// maxCharsPerDoc / maxTokensPerDoc are included because the server truncates
// docText to those caps BEFORE scoring; bumping a cap (e.g. 2000 → 4000) would
// otherwise return stale scores under the same key.
//
// Why SHA-256: FIPS-compatible, collision-resistant for arbitrarily long inputs,
// produces a fixed 64-char hex string that fits any key-value store.
func cacheKey(model, serverNormalize, queryInstr, docInstr, query, docText string, maxCharsPerDoc, maxTokensPerDoc int) string {
	h := sha256.New()
	h.Write([]byte(cacheKeyVersion))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(serverNormalize))
	h.Write([]byte{0})
	h.Write([]byte(queryInstr))
	h.Write([]byte{0})
	h.Write([]byte(docInstr))
	h.Write([]byte{0})
	h.Write([]byte(query))
	h.Write([]byte{0})
	h.Write([]byte(docText))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(maxCharsPerDoc)))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(maxTokensPerDoc)))
	return hex.EncodeToString(h.Sum(nil))
}
