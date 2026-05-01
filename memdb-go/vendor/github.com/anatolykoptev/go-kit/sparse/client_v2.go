package sparse

import (
	"context"
	"fmt"
	"time"
)

// NewClient is the v2 entry point — returns a *Client configured via
// functional options.
//
// url is the embed-server base URL (no /embed_sparse path).
//
// At least one backend-specific Opt must be applied; otherwise NewClient
// returns an error from the underlying constructor.
func NewClient(url string, opts ...Opt) (*Client, error) {
	cfg := defaultCfg()
	cfg.url = url
	for _, opt := range opts {
		opt(cfg)
	}
	return newClientFromInternal(cfg)
}

// newClientFromInternal builds a *Client from an already-resolved
// cfgInternal.
func newClientFromInternal(cfg *cfgInternal) (*Client, error) {
	inner, err := newFromInternal(cfg)
	if err != nil {
		return nil, err
	}
	model := modelFromEmbedder(inner)

	var cb *CircuitBreaker
	if cfg.circuit != nil {
		cbCfg := cfg.circuit.cfg
		cb = NewCircuitBreaker(cbCfg, model, makeCircuitHook(model, cfg.observer))
	}

	vocabSize := cfg.vocabSize
	if vocabSize == 0 && inner != nil {
		vocabSize = inner.VocabSize()
	}

	return &Client{
		inner:     inner,
		observer:  cfg.observer,
		logger:    cfg.logger,
		model:     model,
		vocabSize: vocabSize,
		topK:      cfg.topK,
		minWeight: cfg.minWeight,
		retry:     cfg.retry,
		circuit:   cb,
		fallback:  cfg.fallback,
		cache:     cfg.cache,
	}, nil
}

// EmbedOpt is a per-call option for EmbedSparseWithResult.
type EmbedOpt func(*embedCallCfg)

type embedCallCfg struct {
	DryRun bool
}

// WithDryRun skips the backend call entirely and returns Status=Skipped
// vectors. For testing pipeline wiring without a live server.
func WithDryRun() EmbedOpt {
	return func(c *embedCallCfg) { c.DryRun = true }
}

// EmbedSparseWithResult is the v2 EmbedSparse API — returns a typed Result
// with Status and fires Observer hooks around the backend call.
//
// Lifecycle:
//
//	OnBeforeEmbed → (cache check) → (fallback check) → callBackendResilient → OnAfterEmbed
func (c *Client) EmbedSparseWithResult(ctx context.Context, texts []string, opts ...EmbedOpt) (*Result, error) {
	if c != nil && c.fallback != nil {
		callCfg := embedCallCfg{}
		for _, o := range opts {
			o(&callCfg)
		}
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
// consulting the fallback chain. Used internally by embedWithFallback to
// avoid recursion.
func (c *Client) embedWithResultUnchained(ctx context.Context, texts []string, opts ...EmbedOpt) *Result {
	callCfg := embedCallCfg{}
	for _, o := range opts {
		o(&callCfg)
	}

	if c == nil || c.inner == nil {
		return &Result{Status: StatusSkipped, Model: ""}
	}
	if len(texts) == 0 {
		return &Result{Status: StatusSkipped, Model: c.model}
	}
	if callCfg.DryRun {
		return dryRunResult(c, len(texts))
	}

	// Cache: full-batch lookup before backend call.
	if c.cache != nil {
		cached := tryCacheFullBatchGet(ctx, c.cache, c.model, c.topK, c.minWeight, c.vocabSize, texts)
		if cached != nil {
			safeCall(func() { c.observer.OnCacheHit(ctx, len(texts)) })
			recordCacheHit(c.model)
			out := make([]*Vector, len(cached))
			for i, v := range cached {
				out[i] = &Vector{Sparse: v, Status: StatusOk}
			}
			return &Result{Vectors: out, Status: StatusOk, Model: c.model}
		}
		recordCacheMiss(c.model)
	}

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
		partialErr := fmt.Errorf("sparse: backend returned %d vectors, expected %d", len(raw), len(texts))
		safeCall(func() { c.observer.OnAfterEmbed(ctx, StatusDegraded, dur, len(texts)) })
		return &Result{
			Vectors: emptyVectors(len(texts)),
			Status:  StatusDegraded,
			Model:   c.model,
			Err:     partialErr,
		}
	}

	if c.cache != nil {
		for i, vec := range raw {
			c.cache.Set(ctx, cacheKey(c.model, c.topK, c.minWeight, c.vocabSize, texts[i]), vec)
		}
		recordCacheSet(c.model, len(raw))
	}

	out := make([]*Vector, len(raw))
	for i, v := range raw {
		out[i] = &Vector{Sparse: v, Status: StatusOk}
	}
	safeCall(func() { c.observer.OnAfterEmbed(ctx, StatusOk, dur, len(out)) })
	return &Result{Vectors: out, Status: StatusOk, Model: c.model}
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
	return &Result{Vectors: zeros, Status: StatusSkipped, Model: model}
}

// modelFromEmbedder returns the backend model name when available.
//
// Resolution order:
//  1. Model() string interface (HTTPSparseEmbedder satisfies this).
//  2. "" otherwise.
func modelFromEmbedder(e SparseEmbedder) string {
	if e == nil {
		return ""
	}
	type modelGetter interface{ Model() string }
	if m, ok := e.(modelGetter); ok {
		return m.Model()
	}
	return ""
}

// emptyVectors returns n placeholder Vector entries with Status=StatusSkipped.
func emptyVectors(n int) []*Vector {
	out := make([]*Vector, n)
	for i := range out {
		out[i] = &Vector{Status: StatusSkipped}
	}
	return out
}
