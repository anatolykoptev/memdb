package server

// server_init_search.go — search, embedder and LLM component initialization.
// Covers: initEmbedder, initSearchService, initLLMExtractor.

import (
	"log/slog"

	"github.com/anatolykoptev/go-kit/rerank"
	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/handlers"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

// initEmbedder initializes the embedder via factory (non-fatal if unavailable).
// When ONNXModelDirCode is set, also loads a second ONNX model and creates a Registry.
func initEmbedder(cfg *config.Config, h *handlers.Handler, logger *slog.Logger) embedder.Embedder {
	embCfg := embedder.Config{
		Type:         cfg.EmbedderType,
		ONNXModelDir: cfg.ONNXModelDir,
		VoyageAPIKey: cfg.VoyageAPIKey,
		Model:        cfg.EmbedderModel,
		OllamaURL:    cfg.OllamaURL,
		OllamaDim:    cfg.OllamaDim,
		OllamaPrefix: cfg.OllamaPrefix,
		OllamaQuery:  cfg.OllamaQuery,
		HTTPBaseURL:  cfg.EmbedURL,
	}
	e, err := embedder.New(embCfg, logger)
	if err != nil {
		logger.Warn("embedder init failed (native search disabled)", slog.Any("error", err))
		return nil
	}
	h.SetEmbedder(e)

	// Multi-model registry: HTTP embedder uses sidecar(s) for models;
	// EmbedURLCode overrides jina URL when set (separate Python sidecar).
	if cfg.EmbedderType == "http" && cfg.EmbedURL != "" {
		registry := embedder.NewRegistry("multilingual-e5-large")
		registry.Register("multilingual-e5-large", e)

		codeURL := cfg.EmbedURL
		if cfg.EmbedURLCode != "" {
			codeURL = cfg.EmbedURLCode
		}
		codeEmb := embedder.NewHTTPEmbedder(codeURL, "jina-code-v2", 768, logger)
		registry.Register("jina-code-v2", codeEmb)
		logger.Info("code embedder loaded (http)",
			slog.String("model", "jina-code-v2"),
			slog.String("url", codeURL),
			slog.Int("dim", 768),
		)
		h.SetEmbedRegistry(registry)
	} else if cfg.ONNXModelDirCode != "" {
		registry := embedder.NewRegistry("multilingual-e5-large")
		registry.Register("multilingual-e5-large", e)

		codeCfg, ok := embedder.KnownONNXModels()["jina-code-v2"]
		if !ok {
			codeCfg = embedder.ONNXModelConfig{Dim: 768, MaxLen: 512, PadID: 0}
		}
		codeEmb, codeErr := embedder.NewONNXEmbedder(cfg.ONNXModelDirCode, codeCfg, logger)
		if codeErr != nil {
			logger.Warn("code embedder init failed", slog.Any("error", codeErr))
		} else {
			registry.Register("jina-code-v2", codeEmb)
			logger.Info("code embedder loaded",
				slog.String("model", "jina-code-v2"),
				slog.Int("dim", codeCfg.Dim),
			)
		}
		h.SetEmbedRegistry(registry)
	}

	return e
}

// initSearchService creates the SearchService and wires up optional LLM features and profiler.
func initSearchService(
	cfg *config.Config,
	pg *db.Postgres,
	qd *db.Qdrant,
	emb embedder.Embedder,
	rd *db.Redis,
	h *handlers.Handler,
	logger *slog.Logger,
) (*search.SearchService, *scheduler.Profiler) {
	svc := search.NewSearchService(pg, qd, emb, logger)

	// Cross-encoder rerank client (step 6.05). Zero URL disables the step.
	// APIKey supports hosted providers (Cohere/Jina/Voyage/Mixedbread);
	// leave empty for self-hosted TEI/embed-server. MaxCharsPerDoc caps
	// per-doc length (rune-aware) to bound O(seq²) attention compute.
	svc.RerankClient = rerank.New(rerank.Config{
		URL:            cfg.CrossEncoderURL,
		Model:          cfg.CrossEncoderModel,
		APIKey:         cfg.CrossEncoderAPIKey,
		Timeout:        cfg.CrossEncoderTimeout,
		MaxDocs:        cfg.CrossEncoderMaxDocs,
		MaxCharsPerDoc: cfg.CrossEncoderMaxCharsPerDoc,
	}, logger)
	if svc.RerankClient.Available() {
		logger.Info("cross_encoder rerank enabled",
			slog.String("url", cfg.CrossEncoderURL),
			slog.String("model", cfg.CrossEncoderModel),
			slog.Bool("auth", cfg.CrossEncoderAPIKey != ""),
			slog.Duration("timeout", cfg.CrossEncoderTimeout),
			slog.Int("max_docs", cfg.CrossEncoderMaxDocs),
			slog.Int("max_chars_per_doc", cfg.CrossEncoderMaxCharsPerDoc),
		)
	}

	if cfg.LLMProxyURL != "" {
		svc.LLMReranker = search.LLMRerankConfig{
			APIURL: cfg.LLMProxyURL, APIKey: cfg.LLMProxyAPIKey, Model: cfg.LLMSearchModel,
		}
		svc.Iterative = search.IterativeConfig{
			APIURL: cfg.LLMProxyURL, APIKey: cfg.LLMProxyAPIKey, Model: cfg.LLMSearchModel,
		}
		svc.Enhance = search.EnhanceConfig{
			APIURL: cfg.LLMProxyURL, APIKey: cfg.LLMProxyAPIKey, Model: cfg.LLMSearchModel,
		}
		svc.Fine = search.FineConfig{
			APIURL: cfg.LLMProxyURL, APIKey: cfg.LLMProxyAPIKey, Model: cfg.LLMSearchModel,
		}
		logger.Info("fine search mode enabled")
	}

	if cfg.SearXNGURL != "" {
		bc := initBrowserClient(cfg, logger)
		svc.Internet = search.NewInternetSearcher(search.InternetSearcherConfig{
			SearXNGURL: cfg.SearXNGURL,
			Limit:      search.DefaultInternetLimit,
			Browser:    bc,
		})
		logger.Info("internet search enabled",
			slog.String("searxng_url", cfg.SearXNGURL),
			slog.Bool("direct_scraping", bc != nil),
		)
	}

	var profiler *scheduler.Profiler
	if rd != nil && cfg.LLMProxyURL != "" {
		profiler = scheduler.NewProfiler(pg, rd, cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMDefaultModel, logger)
		svc.Profiler = profiler
		h.SetProfiler(profiler)
		logger.Info("user profile summarizer initialized")
	}

	return svc, profiler
}

// initLLMExtractor creates the LLM extractor for fine-mode native add (non-fatal if URL not set).
func initLLMExtractor(cfg *config.Config, h *handlers.Handler, logger *slog.Logger) *llm.LLMExtractor {
	if cfg.LLMProxyURL == "" {
		return nil
	}
	client := llm.NewClient(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMExtractModel, cfg.LLMFallbackModels, logger)
	extractor := llm.NewLLMExtractorWithClient(client)
	h.SetLLMExtractor(extractor)
	logger.Info("llm extractor initialized",
		slog.String("model", extractor.Model()),
		slog.String("url", cfg.LLMProxyURL),
		slog.Any("fallback_models", cfg.LLMFallbackModels),
	)
	return extractor
}
