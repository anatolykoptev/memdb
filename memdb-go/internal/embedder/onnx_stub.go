//go:build !cgo

package embedder

import (
	"context"
	"fmt"
	"log/slog"
)

// ONNXEmbedder is a stub when CGO is disabled.
type ONNXEmbedder struct{}

// NewONNXEmbedder returns an error when CGO is disabled.
func NewONNXEmbedder(modelDir string, logger *slog.Logger) (*ONNXEmbedder, error) {
	return nil, fmt.Errorf("ONNX embedder requires CGO (libtokenizers + libonnxruntime)")
}

func (e *ONNXEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, fmt.Errorf("ONNX embedder not available: built without CGO")
}

func (e *ONNXEmbedder) Dimension() int { return 0 }
func (e *ONNXEmbedder) Close() error   { return nil }
