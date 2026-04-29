// Backward-compat shim for the ONNX backend. Every public symbol here is a
// thin wrapper around github.com/anatolykoptev/go-kit/embed/onnx. New code
// MUST import the onnx subpackage directly.

package embedder

import (
	"log/slog"

	"github.com/anatolykoptev/go-kit/embed/onnx"
)

// ONNXEmbedder is an alias for [onnx.Embedder].
//
// Deprecated: import github.com/anatolykoptev/go-kit/embed/onnx and use onnx.Embedder.
type ONNXEmbedder = onnx.Embedder

// ONNXModelConfig is an alias for [onnx.ModelConfig].
//
// Deprecated: import github.com/anatolykoptev/go-kit/embed/onnx and use onnx.ModelConfig.
type ONNXModelConfig = onnx.ModelConfig

// DefaultONNXConfig is a re-export of [onnx.DefaultModelConfig].
//
// Deprecated: call onnx.DefaultModelConfig directly.
var DefaultONNXConfig = onnx.DefaultModelConfig

// KnownONNXModels returns the model registry from go-kit/embed/onnx.
//
// Deprecated: call onnx.KnownModels directly.
func KnownONNXModels() map[string]ONNXModelConfig {
	return onnx.KnownModels()
}

// NewONNXEmbedder bridges the legacy (modelDir, ONNXModelConfig, logger)
// signature to the new [onnx.New] which takes an [onnx.Config] struct.
//
// Deprecated: call onnx.New(onnx.Config{ModelDir, Model}, logger) directly.
func NewONNXEmbedder(modelDir string, cfg ONNXModelConfig, logger *slog.Logger) (*ONNXEmbedder, error) {
	return onnx.New(onnx.Config{ModelDir: modelDir, Model: cfg}, logger)
}
