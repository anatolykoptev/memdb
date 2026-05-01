package sparse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
)

// SparseCache abstracts a (text → SparseVector) lookup table.
//
// go-kit/sparse ships NO concrete implementation — callers wire LRU /
// Redis / sync.Map per their runtime. Implementations MUST be safe for
// concurrent reads and writes.
//
// SparseCache is a sibling of embed.Cache rather than a reuse of it: the
// value type is SparseVector (Indices + Values), not []float32, so the
// signature differs at the type level. Encoding is the cache
// implementation's concern — gob, MessagePack, or a compact custom layout
// (varint-prefixed indices + IEEE 754 values) all work.
//
// Cache key invalidation on (model, top_k, min_weight, vocab_size, text)
// change is automatic — see cacheKey below.
//
// Trade-off — SPLADE traffic shape: indexing fresh documents sees each
// text once, so the cache hit ratio is near zero on the document path and
// caching is wasted RAM there. Caching IS valuable on the *query* path
// where the same query may repeat across users / sessions. Because the
// indexing-vs-query split is a caller concern (the SparseEmbedder doesn't
// know which side of the pipeline it's on), the cache is opt-in via
// WithCache and disabled by default — embed/'s pattern.
type SparseCache interface {
	// Get returns the cached SparseVector for the given key. ok=false if
	// not cached. Implementations must NOT panic on ctx cancellation;
	// return ok=false instead.
	Get(ctx context.Context, key string) (SparseVector, bool)
	// Set stores the vector for the given key. Idempotent. Implementations
	// may TTL or evict per their policy.
	Set(ctx context.Context, key string, v SparseVector)
}

// WithCache wires a SparseCache. When set, every (model, top_k,
// min_weight, vocab_size, text) tuple is looked up before the backend
// EmbedSparse call. Full-batch hit short-circuits the backend entirely.
// Partial misses fall through to the backend for the full batch (no
// cherry-pick; keeps the API symmetric and simple). A nil SparseCache is
// ignored (caching stays disabled).
func WithCache(c SparseCache) Opt {
	return func(cfg *cfgInternal) {
		if c != nil {
			cfg.cache = c
		}
	}
}

// cacheKey computes the deterministic key for a (model, top_k, min_weight,
// vocab_size, text) tuple.
//
// Format: sha256(model NUL itoa(top_k) NUL ftoa(min_weight) NUL
// itoa(vocab_size) NUL text). Hex-encoded 64-char string.
//
// Why all 5 fields:
//
//	model       — different SPLADE models produce different vector spaces
//	top_k       — caps the number of returned terms; same text → different
//	              vector at top_k=64 vs top_k=256
//	min_weight  — drops entries below threshold; changes shape per text
//	vocab_size  — informational, but bumping it implies a model swap
//	text        — the input itself
//
// SHA-256: FIPS-compatible, collision-resistant for arbitrary inputs.
// NUL separator: prevents collision when a field value contains another's
// boundary.
func cacheKey(model string, topK int, minWeight float32, vocabSize int, text string) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(itoa(topK)))
	h.Write([]byte{0})
	h.Write([]byte(ftoa(minWeight)))
	h.Write([]byte{0})
	h.Write([]byte(itoa(vocabSize)))
	h.Write([]byte{0})
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

// tryCacheFullBatchGet returns a slice of SparseVectors (ordered matching
// texts[]) if ALL texts have cached entries. Returns nil on any miss — the
// caller must fall through to the backend for the full batch.
func tryCacheFullBatchGet(ctx context.Context, cache SparseCache, model string, topK int, minWeight float32, vocabSize int, texts []string) []SparseVector {
	out := make([]SparseVector, len(texts))
	for i, t := range texts {
		if ctx.Err() != nil {
			return nil
		}
		v, ok := cache.Get(ctx, cacheKey(model, topK, minWeight, vocabSize, t))
		if !ok {
			return nil
		}
		out[i] = v
	}
	return out
}

// ftoa serialises a float32 by its IEEE 754 bit pattern formatted as a
// decimal integer. Bit-exact and deterministic — two distinct float32
// values always produce two distinct strings, with no locale or precision
// concerns. Used only as part of cacheKey, never user-facing.
func ftoa(f float32) string {
	return itoa(int(math.Float32bits(f)))
}
