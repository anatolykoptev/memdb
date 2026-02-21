package embedder

import (
	"fmt"
	"log/slog"
)

// Config holds all embedder configuration in one typed struct.
// Populated from environment variables via config.Config.
type Config struct {
	Type         string // "onnx" | "voyage" | "ollama"
	ONNXModelDir string
	VoyageAPIKey string
	Model        string // voyage or ollama model name
	OllamaURL    string
	OllamaDim    int    // 0 = auto-detect from first response
	OllamaPrefix string // client-side document prefix (e.g. "passage: ")
	OllamaQuery  string // client-side query prefix (e.g. "query: ")
}

// New constructs the appropriate Embedder from cfg.
// Returns an error if the type is unknown or required config is missing.
func New(cfg Config, logger *slog.Logger) (Embedder, error) {
	switch cfg.Type {
	case "ollama":
		model := cfg.Model
		if model == "" {
			model = ollamaDefaultModel
		}
		url := cfg.OllamaURL
		if url == "" {
			url = ollamaDefaultURL
		}
		var opts []OllamaOption
		if cfg.OllamaDim > 0 {
			opts = append(opts, WithOllamaDimension(cfg.OllamaDim))
		}
		if cfg.OllamaPrefix != "" {
			opts = append(opts, WithTextPrefix(cfg.OllamaPrefix))
		}
		if cfg.OllamaQuery != "" {
			opts = append(opts, WithQueryPrefix(cfg.OllamaQuery))
		}
		c := NewOllamaClient(url, model, logger, opts...)
		logger.Info("embedder: ollama",
			slog.String("url", url),
			slog.String("model", model),
			slog.String("doc_prefix", cfg.OllamaPrefix),
			slog.String("query_prefix", cfg.OllamaQuery),
		)
		return c, nil

	case "voyage":
		if cfg.VoyageAPIKey == "" {
			return nil, fmt.Errorf("embedder: voyage requires VOYAGE_API_KEY")
		}
		model := cfg.Model
		if model == "" {
			model = defaultModel
		}
		c := NewVoyageClient(cfg.VoyageAPIKey, model, logger)
		logger.Info("embedder: voyage", slog.String("model", model))
		return c, nil

	case "onnx", "":
		if cfg.ONNXModelDir == "" {
			return nil, fmt.Errorf("embedder: onnx requires MEMDB_ONNX_MODEL_DIR")
		}
		e, err := NewONNXEmbedder(cfg.ONNXModelDir, logger)
		if err != nil {
			return nil, fmt.Errorf("embedder: onnx init: %w", err)
		}
		logger.Info("embedder: onnx", slog.String("model_dir", cfg.ONNXModelDir))
		return e, nil

	default:
		return nil, fmt.Errorf("embedder: unknown type %q (valid: onnx, voyage, ollama)", cfg.Type)
	}
}
