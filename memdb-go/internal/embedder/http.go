package embedder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	gokitembed "github.com/anatolykoptev/go-kit/embed"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// HTTPEmbedder calls a remote OpenAI-compatible /v1/embeddings endpoint.
// Designed for the Rust embed-server sidecar on the internal Docker network.
//
// Foundation migration (task #82): HTTP transport is now delegated to
// github.com/anatolykoptev/go-kit/embed.HTTPEmbedder. The public struct type
// is preserved so callers (factory.go, server_init_search.go, tests) compile
// unchanged.
//
// 2026-05-01 — switched the underlying client from the v1 NewHTTPEmbedder
// helper (no opts) to the v2 NewClient(...) entry point so we can wire
// E3 (cache) and optionally E1 (circuit). The cache wraps the deterministic
// (model, dim, prefix, text) → vector lookup in Redis (see redis_cache.go),
// short-circuiting embed-server traffic on idempotent re-embed (reverse-role
// pass, query rewrites, scheduler reorganiser sweeps). Saves the HTTP round
// trip + ONNX inference whenever a vector is already cached.
type HTTPEmbedder struct {
	baseURL string
	model   string
	inner   *gokitembed.Client
	dim     int
	logger  *slog.Logger
}

// HTTPEmbedderOpts collects the optional dependencies wired through
// NewHTTPEmbedderWithOpts. Kept as a struct (not variadic functional opts on
// our wrapper) because the cardinality is small and the call site (factory)
// is the single producer — adding a field is one line at both ends.
type HTTPEmbedderOpts struct {
	// Cache enables Redis-backed embedding cache via go-kit/embed.WithCache.
	// nil disables caching (legacy v1 behaviour). Recommended for the LoCoMo
	// ingest path where reverse-role + reorganiser sweeps re-embed the same
	// (model, text) tuple repeatedly.
	Cache gokitembed.Cache

	// CircuitConfig (zero-value disabled). When set, wraps the client in a
	// circuit breaker that opens after N consecutive backend failures and
	// short-circuits subsequent calls until the recovery window elapses.
	// Useful in prod where an embed-server crash should fail fast instead of
	// stacking 5s timeouts behind every request.
	Circuit *gokitembed.CircuitConfig
}

// NewHTTPEmbedder creates an HTTPEmbedder pointing at baseURL with no
// resiliency opts (legacy entry point — preserved so existing call sites
// and tests compile unchanged). For production-grade wiring with cache +
// circuit breaker use NewHTTPEmbedderWithOpts.
func NewHTTPEmbedder(baseURL, model string, dim int, logger *slog.Logger) *HTTPEmbedder {
	return NewHTTPEmbedderWithOpts(baseURL, model, dim, logger, HTTPEmbedderOpts{})
}

// NewHTTPEmbedderWithOpts is the v2-aware constructor. Wires go-kit/embed's
// optional features (cache, circuit) when the corresponding opt is non-nil.
// baseURL should not include /v1/embeddings — it will be appended
// automatically by the underlying http transport.
func NewHTTPEmbedderWithOpts(baseURL, model string, dim int, logger *slog.Logger, opts HTTPEmbedderOpts) *HTTPEmbedder {
	trimmed := strings.TrimRight(baseURL, "/")
	clientOpts := []gokitembed.Opt{
		gokitembed.WithBackend("http"),
		gokitembed.WithModel(model),
		gokitembed.WithDim(dim),
		gokitembed.WithLogger(logger),
	}
	if opts.Cache != nil {
		clientOpts = append(clientOpts, gokitembed.WithCache(opts.Cache))
	}
	if opts.Circuit != nil {
		clientOpts = append(clientOpts, gokitembed.WithCircuit(*opts.Circuit))
	}
	client, err := gokitembed.NewClient(trimmed, clientOpts...)
	if err != nil {
		// NewClient only fails on programmer error (no backend opt set, etc.).
		// We always pass WithBackend("http") so a failure here is a build-time
		// regression — surface loudly via panic at startup rather than silently
		// fall back to a broken embedder.
		panic(fmt.Sprintf("embedder.NewHTTPEmbedderWithOpts: gokitembed.NewClient failed: %v", err))
	}
	return &HTTPEmbedder{
		baseURL: trimmed,
		model:   model,
		inner:   client,
		dim:     dim,
		logger:  logger,
	}
}

// Embed sends texts to the remote embedding server and returns vectors.
// Delegates to go-kit/embed.HTTPEmbedder which retries transient failures
// (429/502/503/504) with exponential backoff (200ms → 400ms → 800ms, cap 5s,
// 3 attempts total). Non-retriable errors (4xx validation, unmarshal) fail fast.
func (h *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	start := time.Now()
	mx := embedderMetrics()
	mx.BatchSize.Record(ctx, float64(len(texts)),
		metric.WithAttributes(attribute.String("backend", "http")))
	outcome := "success"
	defer func() {
		mx.Duration.Record(ctx, float64(time.Since(start).Milliseconds()),
			metric.WithAttributes(attribute.String("backend", "http")))
		mx.Requests.Add(ctx, 1, metric.WithAttributes(
			attribute.String("backend", "http"),
			attribute.String("outcome", outcome),
		))
	}()

	// 2026-05-01: route through EmbedWithResult, NOT Client.Embed. The plain
	// Client.Embed entry point in go-kit/embed bypasses the cache layer
	// entirely (it calls callBackendResilient directly — see go-kit
	// client.go:45). Only EmbedWithResult performs the WithCache(...) lookup
	// before hitting the backend (client_v2.go:142). Our wrapper MUST use
	// EmbedWithResult or our cache hit-rate stays at 0% no matter how the
	// cache is wired.
	res, err := h.inner.EmbedWithResult(ctx, texts)
	if err != nil {
		outcome = "error"
		var gokitErr *gokitembed.ErrDimMismatch
		if errors.As(err, &gokitErr) {
			recordHTTPDimMismatch(ctx, h.model)
			return nil, &DimMismatchError{
				Got:   gokitErr.Got,
				Want:  gokitErr.Want,
				Model: h.model,
				Index: 0,
			}
		}
		return nil, err
	}
	if res == nil || res.Status != gokitembed.StatusOk {
		outcome = "error"
		if res != nil && res.Err != nil {
			return nil, res.Err
		}
		return nil, fmt.Errorf("embedder.HTTPEmbedder: status=%v with no error", res)
	}
	vecs := make([][]float32, len(res.Vectors))
	for i, v := range res.Vectors {
		if v == nil {
			vecs[i] = nil
			continue
		}
		vecs[i] = v.Embedding
	}

	h.logger.Debug("http embed complete",
		slog.Int("texts", len(texts)),
	)
	return vecs, nil
}

// EmbedQuery embeds a single query string by delegating to Embed.
func (h *HTTPEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return EmbedQueryViaEmbed(ctx, h, text)
}

// Dimension returns the configured embedding dimension.
func (h *HTTPEmbedder) Dimension() int { return h.dim }

// Close is a no-op for the HTTP-based embedder.
func (h *HTTPEmbedder) Close() error { return nil }

// Compile-time interface check.
var _ Embedder = (*HTTPEmbedder)(nil)

// DimMismatchError is returned when an embed-server response carries a
// vector whose length differs from the configured dimension. Callers can
// errors.As() against it to branch on the typed error; the canonical
// observation is the Prometheus counter incremented at the same time.
type DimMismatchError struct {
	Got   int
	Want  int
	Model string
	Index int
}

func (e *DimMismatchError) Error() string {
	return fmt.Sprintf("embed dim mismatch: got=%d want=%d model=%q index=%d",
		e.Got, e.Want, e.Model, e.Index)
}

var (
	httpDimMismatchOnce    sync.Once
	httpDimMismatchCounter metric.Int64Counter
)

// recordHTTPDimMismatch lazily registers and increments the dim-mismatch
// counter. Pulled into its own helper so the hot Embed path stays readable
// and so tests can verify the model label without poking sync.Once internals.
func recordHTTPDimMismatch(ctx context.Context, model string) {
	httpDimMismatchOnce.Do(func() {
		meter := otel.Meter("memdb-go/embedder")
		c, _ := meter.Int64Counter("memdb.embed.dim_mismatch_total",
			metric.WithDescription("HTTP embedder vector-length mismatch (configured dim != response dim). Indicates an embed-server model swap or version skew."),
		)
		httpDimMismatchCounter = c
	})
	if httpDimMismatchCounter == nil {
		return
	}
	httpDimMismatchCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("model", model),
	))
}
