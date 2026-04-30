package embedder

import (
	"context"
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
// unchanged. Resiliency (E1: circuit/fallback) and cache (E3) are wired in
// subsequent tasks (#83, #84).
type HTTPEmbedder struct {
	baseURL string
	model   string
	inner   *gokitembed.HTTPEmbedder
	dim     int
	logger  *slog.Logger
}

// NewHTTPEmbedder creates an HTTPEmbedder pointing at baseURL.
// baseURL should not include /v1/embeddings — it will be appended automatically.
func NewHTTPEmbedder(baseURL, model string, dim int, logger *slog.Logger) *HTTPEmbedder {
	trimmed := strings.TrimRight(baseURL, "/")
	return &HTTPEmbedder{
		baseURL: trimmed,
		model:   model,
		inner:   gokitembed.NewHTTPEmbedder(trimmed, model, dim, logger),
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

	vecs, err := h.inner.Embed(ctx, texts)
	if err != nil {
		outcome = "error"
		return nil, err
	}

	// Safety net (G7-style validation): the wrapped go-kit HTTPEmbedder does
	// NOT validate per-vector dimension on the v1 NewHTTPEmbedder path —
	// validation only fires through the v2 Client built via NewClient +
	// WithDim. memdb-go uses NewHTTPEmbedder directly, so a model swap on
	// the embed-server side (accidental e5-large→jina-code-v2, fork drift)
	// would silently write wrong-dim vectors into pgvector and corrupt the
	// halfvec column without any error surface.
	//
	// dim == 0 disables the check, mirroring go-kit's "WithDim(0) =
	// auto-detect" convention used by ONNX/Voyage paths.
	if h.dim > 0 {
		for i, v := range vecs {
			if len(v) != h.dim {
				outcome = "error"
				recordHTTPDimMismatch(ctx, h.model)
				return nil, &DimMismatchError{
					Got:   len(v),
					Want:  h.dim,
					Model: h.model,
					Index: i,
				}
			}
		}
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
