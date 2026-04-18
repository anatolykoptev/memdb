//go:build !cgo

package embedder

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ONNXEmbedder is a stub when CGO is disabled.
type ONNXEmbedder struct{}

// NewONNXEmbedder returns an error when CGO is disabled.
func NewONNXEmbedder(modelDir string, _ ONNXModelConfig, logger *slog.Logger) (*ONNXEmbedder, error) {
	return nil, fmt.Errorf("ONNX embedder requires CGO (libtokenizers + libonnxruntime)")
}

func (e *ONNXEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	start := time.Now()
	mx := embedderMetrics()
	mx.BatchSize.Record(ctx, float64(len(texts)),
		metric.WithAttributes(attribute.String("backend", "onnx")))
	defer func() {
		mx.Duration.Record(ctx, float64(time.Since(start).Milliseconds()),
			metric.WithAttributes(attribute.String("backend", "onnx")))
		mx.Requests.Add(ctx, 1, metric.WithAttributes(
			attribute.String("backend", "onnx"),
			attribute.String("outcome", "error"),
		))
	}()
	return nil, fmt.Errorf("ONNX embedder not available: built without CGO")
}

func (e *ONNXEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return nil, fmt.Errorf("ONNX embedder not available: built without CGO")
}

func (e *ONNXEmbedder) Dimension() int { return 0 }
func (e *ONNXEmbedder) Close() error   { return nil }
