package embed

import (
	"context"
	"log/slog"
)

// Client wraps an Embedder backend with v2 features: Observer hooks, retry,
// circuit breaker, and multi-model fallback (E1). Built via NewClient(url, opts...).
//
// Client itself implements Embedder, so it is drop-in replaceable for v1
// backends. v1 callers that hold the result as Embedder continue to work
// unchanged; v2 callers cast to *Client to call EmbedWithResult directly.
type Client struct {
	inner    Embedder     // underlying backend (HTTP/Ollama/Voyage/custom)
	observer Observer     // wired via WithObserver, fires lifecycle hooks
	logger   *slog.Logger // optional, defaults to slog.Default()
	model    string       // resolved model name (for Result.Model)

	// E1: resiliency
	retry    RetryPolicy
	circuit  *CircuitBreaker
	fallback *Client

	// E3: pluggable cache; nil = disabled.
	// docPrefix / queryPrefix feed the cache key (E4 will unify across all backends;
	// for now populated from Ollama-specific opts).
	cache       Cache
	docPrefix   string
	queryPrefix string
}

// Embed satisfies the Embedder interface. Delegates to inner (via callBackendResilient
// if circuit is wired). For the full Result API with observer hooks, use EmbedWithResult.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if c == nil || c.inner == nil {
		return nil, nil
	}
	return c.callBackendResilient(ctx, texts)
}

// EmbedQuery satisfies Embedder; delegates to inner.
func (c *Client) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if c == nil || c.inner == nil {
		return nil, nil
	}
	return c.inner.EmbedQuery(ctx, text)
}

// Dimension satisfies Embedder.
func (c *Client) Dimension() int {
	if c == nil || c.inner == nil {
		return 0
	}
	return c.inner.Dimension()
}

// Close satisfies Embedder; closes the inner backend.
func (c *Client) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.Close()
}

// Model returns the resolved model name. Satisfies the optional modelGetter
// interface used by modelFromEmbedder's fallback chain.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// callBackendResilient wraps c.inner.Embed with:
//  1. Circuit breaker check (if configured) — returns ErrCircuitOpen immediately if open.
//  2. Retry loop via do() (default: 3 attempts on 5xx, exp backoff + jitter).
//  3. Circuit breaker feedback (MarkSuccess/MarkFailure if configured).
//
// This is the single wrap point per E1 spec.
func (c *Client) callBackendResilient(ctx context.Context, texts []string) ([][]float32, error) {
	cb := c.circuit

	// 1. Circuit breaker guard.
	if cb != nil && !cb.Allow() {
		recordGiveup(c.model, "circuit_open")
		return nil, ErrCircuitOpen
	}

	// 2. Retry loop.
	raw, err := do(ctx, c.retry, c.model, c.observer, func() ([][]float32, error) {
		return c.inner.Embed(ctx, texts)
	})

	// 3. Circuit breaker feedback.
	if cb != nil {
		if err != nil {
			cb.MarkFailure()
		} else {
			cb.MarkSuccess()
		}
	}

	return raw, err
}

// Compile-time interface satisfaction.
var _ Embedder = (*Client)(nil)
