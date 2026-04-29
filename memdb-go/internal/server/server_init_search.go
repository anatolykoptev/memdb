package server

// server_init_search.go — search, embedder and LLM component initialization.
// Covers: initEmbedder, initSearchService, initLLMExtractor.

import (
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/anatolykoptev/go-kit/embed"
	"github.com/anatolykoptev/go-kit/embed/onnx"
	"github.com/anatolykoptev/go-kit/rerank"
	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/handlers"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

// initEmbedder initializes the embedder (non-fatal if unavailable).
//
// HTTP / Ollama / Voyage backends are constructed via embed.New. The "onnx"
// backend lives in a sibling subpackage (embed/onnx) — embed.New returns
// ErrONNXNotInFactory for type="onnx" and we wire onnx.New explicitly to
// keep the cgo dependency confined to processes that actually need it.
//
// When ONNXModelDirCode is set, also loads a second ONNX model and creates
// a Registry.
func initEmbedder(cfg *config.Config, h *handlers.Handler, logger *slog.Logger) embed.Embedder {
	e, err := newPrimaryEmbedder(cfg, logger)
	if err != nil {
		logger.Warn("embedder init failed (native search disabled)", slog.Any("error", err))
		return nil
	}
	h.SetEmbedder(e)

	// Multi-model registry: HTTP embedder uses sidecar(s) for models;
	// EmbedURLCode overrides jina URL when set (separate Python sidecar).
	switch {
	case cfg.EmbedderType == "http" && cfg.EmbedURL != "":
		registry := embed.NewRegistry("multilingual-e5-large")
		registry.Register("multilingual-e5-large", e)

		codeURL := cfg.EmbedURL
		if cfg.EmbedURLCode != "" {
			codeURL = cfg.EmbedURLCode
		}
		codeEmb := embed.NewHTTPEmbedder(codeURL, "jina-code-v2", 768, logger)
		registry.Register("jina-code-v2", codeEmb)
		logger.Info("code embedder loaded (http)",
			slog.String("model", "jina-code-v2"),
			slog.String("url", codeURL),
			slog.Int("dim", 768),
		)
		h.SetEmbedRegistry(registry)
	case cfg.ONNXModelDirCode != "":
		registry := embed.NewRegistry("multilingual-e5-large")
		registry.Register("multilingual-e5-large", e)

		codeCfg, ok := onnx.KnownModels()["jina-code-v2"]
		if !ok {
			codeCfg = onnx.ModelConfig{Dim: 768, MaxLen: 512, PadID: 0}
		}
		codeEmb, codeErr := onnx.New(onnx.Config{ModelDir: cfg.ONNXModelDirCode, Model: codeCfg}, logger)
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

// newPrimaryEmbedder dispatches the primary embedder based on cfg.EmbedderType.
// "onnx" / "" goes through the onnx subpackage (cgo); everything else uses embed.New.
func newPrimaryEmbedder(cfg *config.Config, logger *slog.Logger) (embed.Embedder, error) {
	if cfg.EmbedderType == "onnx" || cfg.EmbedderType == "" {
		if cfg.ONNXModelDir == "" {
			return nil, errors.New("embedder: onnx requires MEMDB_ONNX_MODEL_DIR")
		}
		modelCfg := onnx.DefaultModelConfig()
		if mc, ok := onnx.KnownModels()[cfg.EmbedderModel]; ok {
			modelCfg = mc
		}
		e, err := onnx.New(onnx.Config{ModelDir: cfg.ONNXModelDir, Model: modelCfg}, logger)
		if err != nil {
			return nil, err
		}
		logger.Info("embedder: onnx", slog.String("model_dir", cfg.ONNXModelDir))
		return e, nil
	}
	return embed.New(embed.Config{
		Type:         cfg.EmbedderType,
		ONNXModelDir: cfg.ONNXModelDir,
		VoyageAPIKey: cfg.VoyageAPIKey,
		Model:        cfg.EmbedderModel,
		OllamaURL:    cfg.OllamaURL,
		OllamaDim:    cfg.OllamaDim,
		OllamaPrefix: cfg.OllamaPrefix,
		OllamaQuery:  cfg.OllamaQuery,
		HTTPBaseURL:  cfg.EmbedURL,
	}, logger)
}

// initSearchService creates the SearchService and wires up optional LLM features and profiler.
func initSearchService(
	cfg *config.Config,
	pg *db.Postgres,
	qd *db.Qdrant,
	emb embed.Embedder,
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

		// D11 CoT decomposer — env-gated (MEMDB_COT_DECOMPOSE), default OFF.
		// Constructed unconditionally when the LLM proxy is configured so the
		// gate can flip at runtime without a rebuild; Decompose() short-
		// circuits when cfg.Enabled is false.
		svc.CoTDecomposer = search.NewCoTDecomposer(search.CoTDecomposerConfig{
			APIURL:        cfg.LLMProxyURL,
			APIKey:        cfg.LLMProxyAPIKey,
			Model:         cfg.LLMSearchModel,
			Enabled:       cfg.CoTDecompose,
			MaxSubQueries: cfg.CoTMaxSubqueries,
			Timeout:       time.Duration(cfg.CoTTimeoutMS) * time.Millisecond,
		})
		if cfg.CoTDecompose {
			logger.Info("d11 cot decomposer enabled",
				slog.Int("max_subqueries", cfg.CoTMaxSubqueries),
				slog.Int("timeout_ms", cfg.CoTTimeoutMS),
			)
		}
		// F2 reflection-loop deep-search agent — env-gated by MEMDB_F2_REFLECTION
		// (default OFF). Constructed when LLM proxy is configured so flipping
		// the env at runtime activates the stage without a rebuild. Reuses the
		// same LLMSearchModel as the rest of the search service.
		reflectionClient := llm.NewSimpleClient(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMSearchModel)
		svc.SetReflectionAgent(search.NewReflectionAgent(reflectionClient))
		logger.Info("f2 reflection agent wired (gated by MEMDB_F2_REFLECTION)")

		// D7 (MEMDB_SEARCH_COT) + D11 (MEMDB_COT_DECOMPOSE) both enabled:
		// each query may trigger 2 LLM round-trips + up to 6 extra VectorSearch
		// calls. Fine for ablation; not recommended for production.
		if os.Getenv("MEMDB_SEARCH_COT") == "true" && cfg.CoTDecompose {
			logger.Warn("both D7 (search_cot) and D11 (cot_decompose) enabled — expect 2 LLM round-trips + up to 6 extra VectorSearch calls per query; intended for ablation, not prod")
		}
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
