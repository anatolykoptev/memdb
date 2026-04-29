package embed

import (
	"context"
	"fmt"
	"time"
)

// NewClient is the v2 entry point — returns a *Client configured via
// functional options. v1 callers continue to use New(cfg, logger) which
// calls the per-backend helpers directly.
//
// url is the backend URL when applicable. For Ollama/HTTP backends, pass the
// base URL. For Voyage, url is ignored (endpoint is hardcoded by the API).
// For ONNX, use the embed/onnx subpackage directly.
//
// At least one backend-specific Opt must be applied; otherwise NewClient
// returns an error from the underlying constructor.
//
// The returned *Client implements Embedder, so it is assignable to an Embedder
// variable for v1-style callers. Cast to *Client to access EmbedWithResult.
func NewClient(url string, opts ...Opt) (*Client, error) {
	cfg := defaultCfg()
	cfg.url = url
	for _, opt := range opts {
		opt(cfg)
	}
	return newClientFromInternal(cfg)
}

// newClientFromInternal builds a *Client from an already-resolved cfgInternal.
// Used by both NewClient (v2) and the v1 New() wrapper after option translation.
// E1: finalises the CircuitBreaker wiring (model + observer hook) now that all
// options have been applied.
func newClientFromInternal(cfg *cfgInternal) (*Client, error) {
	inner, err := newFromInternal(cfg)
	if err != nil {
		return nil, err
	}
	model := modelFromEmbedder(inner)

	// E1: wire circuit breaker. If WithCircuit set a sentinel CB, rebuild it
	// with the final model name and observer so the transition hook works.
	var cb *CircuitBreaker
	if cfg.circuit != nil {
		cbCfg := cfg.circuit.cfg
		cb = NewCircuitBreaker(cbCfg, model, makeCircuitHook(model, cfg.observer))
	}

	return &Client{
		inner:       inner,
		observer:    cfg.observer,
		logger:      cfg.logger,
		model:       model,
		expectedDim: cfg.dim,
		retry:       cfg.retry,
		circuit:     cb,
		fallback:    cfg.fallback,
		cache:       cfg.cache,
		docPrefix:   cfg.ollamaDocPrefix,
		queryPrefix: cfg.ollamaQueryPrefix,
	}, nil
}

// EmbedOpt is a per-call option for EmbedWithResult.
type EmbedOpt func(*embedCallCfg)

type embedCallCfg struct {
	DryRun bool
}

// WithDryRun skips the backend call entirely and returns Status=Skipped vectors
// of zero length. For testing pipeline wiring without a live server.
func WithDryRun() EmbedOpt {
	return func(c *embedCallCfg) { c.DryRun = true }
}

// EmbedWithResult is the v2 Embed API — returns a typed Result with Status and
// fires Observer hooks around the backend call.
//
// Lifecycle:
//
//	OnBeforeEmbed → (fallback check) → callBackendResilient → OnAfterEmbed
//
// Status semantics:
//   - StatusOk       — request succeeded, vectors are valid
//   - StatusDegraded — request failed, Err is set
//   - StatusFallback — primary degraded, secondary succeeded (E1)
//   - StatusSkipped  — nil inner, empty texts, or DryRun enabled
//
// E1 wires retry/circuit/fallback on top of this call.
// E2 wires auto-batching, E3 wires cache, E4 wires per-text Status reasoning.
func (c *Client) EmbedWithResult(ctx context.Context, texts []string, opts ...EmbedOpt) (*Result, error) {
	// E1: if fallback is configured, route through embedWithFallback.
	if c != nil && c.fallback != nil {
		callCfg := embedCallCfg{}
		for _, o := range opts {
			o(&callCfg)
		}
		// DryRun shortcut still fires before fallback routing.
		if callCfg.DryRun {
			return dryRunResult(c, len(texts)), nil
		}
		res := embedWithFallback(ctx, c, c.fallback, texts, opts...)
		if res.Status == StatusDegraded {
			return res, res.Err
		}
		return res, nil
	}
	res := c.embedWithResultUnchained(ctx, texts, opts...)
	if res.Status == StatusDegraded {
		return res, res.Err
	}
	return res, nil
}

// embedWithResultUnchained executes the embed call for this client WITHOUT
// consulting the fallback chain. Used internally by embedWithFallback to avoid
// recursion. External callers always go through EmbedWithResult.
func (c *Client) embedWithResultUnchained(ctx context.Context, texts []string, opts ...EmbedOpt) *Result {
	callCfg := embedCallCfg{}
	for _, o := range opts {
		o(&callCfg)
	}

	if c == nil || c.inner == nil {
		return &Result{Status: StatusSkipped, Model: ""}
	}
	if len(texts) == 0 {
		return &Result{
			Status: StatusSkipped,
			Model:  c.model,
		}
	}
	if callCfg.DryRun {
		return dryRunResult(c, len(texts))
	}

	// E3: full-batch cache lookup BEFORE backend call.
	// Full hit → skip callBackendResilient entirely (no retry, no circuit, no backend).
	// Partial miss (any single text absent) → fall through to backend for the full batch.
	if c.cache != nil {
		dim := c.inner.Dimension()
		cached := tryCacheFullBatchGet(ctx, c.cache, c.model, dim, c.docPrefix, c.queryPrefix, texts)
		if cached != nil {
			safeCall(func() { c.observer.OnCacheHit(ctx, len(texts)) })
			recordCacheHit(c.model)
			out := make([]*Vector, len(cached))
			for i, v := range cached {
				out[i] = &Vector{
					Embedding: v,
					Dim:       len(v),
					Status:    StatusOk,
				}
			}
			return &Result{
				Vectors: out,
				Status:  StatusOk,
				Model:   c.model,
			}
		}
		recordCacheMiss(c.model)
	}

	// Fire OnBeforeEmbed hook (panic-safe).
	safeCall(func() { c.observer.OnBeforeEmbed(ctx, c.model, len(texts)) })

	start := time.Now()
	raw, err := c.callBackendResilient(ctx, texts)
	dur := time.Since(start)

	if err != nil {
		safeCall(func() { c.observer.OnAfterEmbed(ctx, StatusDegraded, dur, len(texts)) })
		return &Result{
			Vectors: emptyVectors(len(texts)),
			Status:  StatusDegraded,
			Model:   c.model,
			Err:     err,
		}
	}

	if len(raw) != len(texts) {
		partialErr := fmt.Errorf("embed: backend returned %d vectors, expected %d", len(raw), len(texts))
		safeCall(func() { c.observer.OnAfterEmbed(ctx, StatusDegraded, dur, len(texts)) })
		return &Result{
			Vectors: emptyVectors(len(texts)),
			Status:  StatusDegraded,
			Model:   c.model,
			Err:     partialErr,
		}
	}

	// E3: populate cache after successful backend call (raw vectors, pre-pipeline).
	if c.cache != nil {
		dim := c.inner.Dimension()
		for i, vec := range raw {
			c.cache.Set(ctx, cacheKey(c.model, dim, c.docPrefix, c.queryPrefix, texts[i]), vec)
		}
		recordCacheSet(c.model, len(raw))
	}

	out := make([]*Vector, len(raw))
	for i, v := range raw {
		out[i] = &Vector{
			Embedding: v,
			Dim:       len(v),
			Status:    StatusOk,
		}
	}
	safeCall(func() { c.observer.OnAfterEmbed(ctx, StatusOk, dur, len(out)) })
	return &Result{
		Vectors: out,
		Status:  StatusOk,
		Model:   c.model,
	}
}

// dryRunResult returns a StatusSkipped Result with zero-value Vector entries.
func dryRunResult(c *Client, n int) *Result {
	zeros := make([]*Vector, n)
	for i := range zeros {
		zeros[i] = &Vector{Status: StatusSkipped}
	}
	model := ""
	if c != nil {
		model = c.model
	}
	return &Result{
		Vectors: zeros,
		Status:  StatusSkipped,
		Model:   model,
	}
}

// EmbedWithResult is the package-level v2 API shim — kept for backward
// compatibility with callers using the old free-function signature.
//
// If e is a *Client, its EmbedWithResult method is called directly (observer
// hooks fire). For any other Embedder, a temporary *Client wrapper is created
// with no observer wired — hooks are silent. New code should use
// NewClient(...).EmbedWithResult(...) directly.
//
// Deprecated: use (*Client).EmbedWithResult for new code.
func EmbedWithResult(ctx context.Context, e Embedder, texts []string, opts ...EmbedOpt) (*Result, error) {
	if c, ok := e.(*Client); ok {
		return c.EmbedWithResult(ctx, texts, opts...)
	}
	// Fallback: wrap in a temporary Client (no observer).
	tmp := &Client{
		inner:    e,
		observer: noopObserver{},
		model:    modelFromEmbedder(e),
		retry:    defaultRetryPolicy(),
	}
	return tmp.EmbedWithResult(ctx, texts, opts...)
}

// modelFromEmbedder returns the backend model name when available.
// Resolution order:
//  1. Model() string interface — caller-supplied or custom Embedder that
//     exposes its model name (e.g. future embed/onnx extension).
//  2. Concrete type-switch for built-in backends (HTTPEmbedder, OllamaClient,
//     VoyageClient) — avoids requiring a public Model() method on each type.
//  3. Falls back to "" for unknown / opaque types.
func modelFromEmbedder(e Embedder) string {
	if e == nil {
		return ""
	}
	type modelGetter interface{ Model() string }
	if m, ok := e.(modelGetter); ok {
		return m.Model()
	}
	switch v := e.(type) {
	case *HTTPEmbedder:
		return v.model
	case *OllamaClient:
		return v.model
	case *VoyageClient:
		return v.model
	default:
		return ""
	}
}

// emptyVectors returns n placeholder Vector entries with Status=StatusSkipped.
func emptyVectors(n int) []*Vector {
	out := make([]*Vector, n)
	for i := range out {
		out[i] = &Vector{Status: StatusSkipped}
	}
	return out
}
