package embedder

import (
	"context"
	"log/slog"
	"strings"
	"time"

	gokitembed "github.com/anatolykoptev/go-kit/embed"
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
