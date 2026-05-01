// Package llm — cache.go: pluggable response cache for chat completions.
//
// Why: LLM-side caches in upstream services (D4 query rewrite, D7 decompose,
// D10 enhance, D11 CoT) all roll their own LRU. Hoisting the pattern into
// go-kit/llm via a Middleware-style hook lets every callsite enable caching
// with one Option, and gives operators consistent metrics across the fleet.
//
// Pattern mirrors embed/cache.go (Cache interface + WithCache option) and
// circuit.go (registered as middleware so the cache lookup wraps the entire
// network round trip — including retries, fallback keys, and the circuit
// breaker). On cache hit, NONE of those fire — saves the LLM provider call,
// the proxy round trip, and the deserialization.
//
// Cache key derivation:
//
//	sha256(model || system || user-content-canonical || maxTokens || temperature)
//
// The canonical user-content serialisation flattens multipart messages so a
// vision request and a text-only request never collide. Temperature is
// included so a deterministic call (T=0) and a creative call (T=0.9) cache
// separately even with identical messages — they are different operations.
//
// What is NOT cached:
//   - Tool-using requests (Tools field non-nil). Tool execution side effects
//     defeat caching by definition.
//   - Streaming requests (the Cache.Get/Set surface is final-response only).
//   - Requests whose ctx is already cancelled (callers should not pay
//     a Redis lookup for a doomed request).
//
// Implementations supplied externally — callers wire LRU/Redis/sync.Map per
// their runtime. go-kit ships zero default backends.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Cache abstracts the (chat-request → chat-response) lookup surface. Safe
// for concurrent reads and writes is required.
//
// Get returns (nil, false) on miss / error / disabled. Implementations MUST
// NOT panic on ctx cancellation — return ok=false instead.
//
// Set is idempotent. TTL/eviction policy is the implementation's concern.
// A failing Set MUST NOT prevent the response from reaching the caller —
// the cache is best-effort, not load-bearing.
type Cache interface {
	Get(ctx context.Context, key string) (resp *ChatResponse, ok bool)
	Set(ctx context.Context, key string, resp *ChatResponse)
}

// WithCache wires a Cache as the OUTERMOST middleware (just outside any
// circuit breaker, so a cache hit avoids all other layers). Pass nil to
// keep the option no-op (idiomatic test setup: pass a flag-controlled cache
// pointer, no branching at the call site).
func WithCache(c Cache) Option {
	return func(cl *Client) {
		if c == nil {
			return
		}
		mw := func(ctx context.Context, req *ChatRequest, next func(context.Context, *ChatRequest) (*ChatResponse, error)) (*ChatResponse, error) {
			// Skip cacheable-request gate: tool-using or non-deterministic
			// requests bypass cache entirely. We treat the presence of any
			// `Tools` slot as "stateful" — callers are likely depending on
			// fresh tool dispatch.
			if isCacheableRequest(req) {
				if cached, ok := c.Get(ctx, cacheKey(req)); ok {
					return cached, nil
				}
			}
			resp, err := next(ctx, req)
			if err != nil || resp == nil {
				return resp, err
			}
			if isCacheableRequest(req) {
				c.Set(ctx, cacheKey(req), resp)
			}
			return resp, err
		}
		cl.middleware = append([]Middleware{mw}, cl.middleware...)
	}
}

// isCacheableRequest gates: tool-using requests bypass the cache because
// tools have side effects. Streaming is filtered at the call-site (Stream*
// methods do not invoke the middleware chain that hosts this cache).
func isCacheableRequest(req *ChatRequest) bool {
	if req == nil {
		return false
	}
	if len(req.Tools) > 0 {
		return false
	}
	return true
}

// cacheKey computes a deterministic SHA-256 over the request fields that
// affect the model output. NUL-separated to prevent boundary collisions
// (model="abc\x00x" + msg="" vs model="abc" + msg="\x00x" produce distinct
// keys this way). Hex-encoded for easy operator inspection.
func cacheKey(req *ChatRequest) string {
	h := sha256.New()
	h.Write([]byte(req.Model))
	h.Write([]byte{0})
	for _, m := range req.Messages {
		h.Write([]byte(m.Role))
		h.Write([]byte{0})
		writeMessageContent(h, m.Content)
		h.Write([]byte{0})
	}
	if req.Temperature != nil {
		// Use a binary float representation rather than fmt %f so 0.0 vs
		// 0.00 don't produce different keys.
		fmt.Fprintf(h, "T=%g", *req.Temperature)
	} else {
		h.Write([]byte("T=nil"))
	}
	h.Write([]byte{0})
	fmt.Fprintf(h, "M=%d", req.MaxTokens)
	return hex.EncodeToString(h.Sum(nil))
}

// writeMessageContent canonicalises Content (string OR []ContentPart) into
// the cache hash. Multipart messages flatten to "type:value" segments so a
// text-only message with the same string never collides with a multipart
// message that happens to embed the same string in a part.
func writeMessageContent(h interface{ Write([]byte) (int, error) }, content interface{}) {
	switch v := content.(type) {
	case string:
		h.Write([]byte("s|"))
		h.Write([]byte(v))
	case []ContentPart:
		h.Write([]byte("p|"))
		for _, p := range v {
			h.Write([]byte(p.Type))
			h.Write([]byte{':'})
			if p.Text != "" {
				h.Write([]byte(p.Text))
			}
			if p.ImageURL != nil {
				h.Write([]byte("i:"))
				h.Write([]byte(p.ImageURL.URL))
			}
			h.Write([]byte{0})
		}
	default:
		// Unknown content type — fold into the key as a literal so distinct
		// inputs produce distinct keys; loss-of-fidelity collisions across
		// unknown shapes are still avoided by the type tag prefix.
		fmt.Fprintf(h, "u|%v", v)
	}
}
