package sparse

import (
	"context"
	"log/slog"
)

// Client wraps a SparseEmbedder backend with v2 features: Observer hooks,
// retry, optional cache, optional circuit breaker, and optional fallback.
// Built via NewClient(url, opts...).
//
// Client itself implements SparseEmbedder, so it is drop-in replaceable
// for v1 backends. v1 callers that hold the result as SparseEmbedder
// continue to work unchanged; v2 callers cast to *Client to call
// EmbedSparseWithResult directly.
type Client struct {
	inner    SparseEmbedder
	observer Observer
	logger   *slog.Logger
	model    string

	vocabSize int
	topK      int
	minWeight float32

	retry    RetryConfig
	circuit  *CircuitBreaker
	fallback *Client

	cache SparseCache
}

// EmbedSparse satisfies the SparseEmbedder interface. Routes through
// EmbedSparseWithResult so cache, circuit, fallback, and observer hooks
// fire identically.
func (c *Client) EmbedSparse(ctx context.Context, texts []string) ([]SparseVector, error) {
	if c == nil || c.inner == nil {
		return nil, nil
	}
	res, err := c.EmbedSparseWithResult(ctx, texts)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	// Empty input or nil-inner produces StatusSkipped with nil Vectors;
	// preserve the v1 contract that empty input returns (nil, nil) — never
	// allocate an empty []SparseVector{} slice the caller might mistake
	// for a real result.
	if res.Status == StatusSkipped {
		return nil, nil
	}
	if res.Status == StatusDegraded && res.Err != nil {
		return nil, res.Err
	}
	out := make([]SparseVector, len(res.Vectors))
	for i, v := range res.Vectors {
		if v == nil {
			continue
		}
		out[i] = v.Sparse
	}
	return out, nil
}

// EmbedSparseQuery satisfies SparseEmbedder; routes through EmbedSparse so
// single-text query embeddings benefit from the same cache + resilience
// layers as batch calls.
func (c *Client) EmbedSparseQuery(ctx context.Context, text string) (SparseVector, error) {
	if c == nil || c.inner == nil {
		return SparseVector{}, nil
	}
	vecs, err := c.EmbedSparse(ctx, []string{text})
	if err != nil {
		return SparseVector{}, err
	}
	if len(vecs) == 0 {
		return SparseVector{}, nil
	}
	return vecs[0], nil
}

// VocabSize satisfies SparseEmbedder. Returns the configured vocab size,
// preferring the value set via WithClientVocabSize if non-zero, otherwise
// the inner backend's report.
func (c *Client) VocabSize() int {
	if c == nil {
		return 0
	}
	if c.vocabSize > 0 {
		return c.vocabSize
	}
	if c.inner == nil {
		return 0
	}
	return c.inner.VocabSize()
}

// Close satisfies SparseEmbedder; closes the inner backend.
func (c *Client) Close() error {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.Close()
}

// Model returns the resolved model name.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// callBackendResilient wraps c.inner.EmbedSparse with:
//  1. Circuit breaker check (if configured) — returns ErrCircuitOpen if open.
//  2. The backend call (retry is applied inside HTTPSparseEmbedder; this
//     layer adds the circuit + observer hooks).
//  3. Circuit breaker feedback (MarkSuccess/MarkFailure if configured).
func (c *Client) callBackendResilient(ctx context.Context, texts []string) ([]SparseVector, error) {
	cb := c.circuit
	if cb != nil && !cb.Allow() {
		recordGiveup(c.model, "circuit_open")
		return nil, ErrCircuitOpen
	}

	raw, err := c.inner.EmbedSparse(ctx, texts)

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
var _ SparseEmbedder = (*Client)(nil)
