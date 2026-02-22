// Package server sets up the HTTP server with all routes and middleware.
package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/MemDBai/MemDB/memdb-go/internal/cache"
	"github.com/MemDBai/MemDB/memdb-go/internal/config"
	"github.com/MemDBai/MemDB/memdb-go/internal/embedder"
	"github.com/MemDBai/MemDB/memdb-go/internal/handlers"
	"github.com/MemDBai/MemDB/memdb-go/internal/llm"
	"github.com/MemDBai/MemDB/memdb-go/internal/rpc"
	"github.com/MemDBai/MemDB/memdb-go/internal/scheduler"
	"github.com/MemDBai/MemDB/memdb-go/internal/search"
	mw "github.com/MemDBai/MemDB/memdb-go/internal/server/middleware"
)

// New creates a fully configured HTTP server and returns a cleanup function
// that closes all database connections on shutdown.
// ctx is used to control the lifetime of background workers (scheduler).
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*http.Server, func()) {
	// Initialize cache client (non-fatal if unavailable)
	var cacheClient *cache.Client
	if cfg.CacheEnabled {
		var err error
		cacheClient, err = cache.New(cfg.RedisURL, logger)
		if err != nil {
			logger.Warn("cache disabled: redis unavailable", slog.Any("error", err))
		}
	}

	// Initialize Python proxy client
	pythonClient := rpc.NewPythonClient(cfg.PythonBackendURL, logger)

	// Initialize handlers
	h := handlers.NewHandler(pythonClient, logger)

	// Initialize database clients for Phase 2 native handlers (non-fatal)
	pg, qd, rd, wmCache := initDBClients(ctx, cfg, h, logger)

	// Initialize embedder via factory (non-fatal: server starts without embedder)
	var emb embedder.Embedder
	embCfg := embedder.Config{
		Type:         cfg.EmbedderType,
		ONNXModelDir: cfg.ONNXModelDir,
		VoyageAPIKey: cfg.VoyageAPIKey,
		Model:        cfg.EmbedderModel,
		OllamaURL:    cfg.OllamaURL,
		OllamaDim:    cfg.OllamaDim,
		OllamaPrefix: cfg.OllamaPrefix,
		OllamaQuery:  cfg.OllamaQuery,
	}
	if e, err := embedder.New(embCfg, logger); err != nil {
		logger.Warn("embedder init failed (native search disabled)", slog.Any("error", err))
	} else {
		emb = e
		h.SetEmbedder(emb)
	}

	// Initialize unified search service
	searchSvc := search.NewSearchService(pg, qd, emb, logger)
	// Enable LLM reranker if CLIProxyAPI is configured (same endpoint as extractor)
	if cfg.LLMProxyURL != "" {
		searchSvc.LLMReranker = search.LLMRerankConfig{
			APIURL: cfg.LLMProxyURL,
			APIKey: cfg.LLMProxyAPIKey,
			Model:  cfg.LLMDefaultModel,
		}
		// Enable iterative multi-stage retrieval (AdvancedSearcher port).
		// NumStages=2 is fast mode (2 expansion rounds after first-pass recall).
		// Per-request override available via SearchParams.NumStages.
		searchSvc.Iterative = search.IterativeConfig{
			APIURL:    cfg.LLMProxyURL,
			APIKey:    cfg.LLMProxyAPIKey,
			Model:     cfg.LLMDefaultModel,
			NumStages: 2,
		}
	}
	// Enable Memobase-style user profile summaries if both LLM and Redis are available.
	// Profiler generates a paragraph profile from UserMemory nodes and caches it 1hr in Redis.
	// TriggerRefresh is called fire-and-forget from add_fine and async worker after each add operation.
	var profiler *scheduler.Profiler
	if rd != nil && cfg.LLMProxyURL != "" {
		profiler = scheduler.NewProfiler(pg, rd, cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMDefaultModel, logger)
		searchSvc.Profiler = profiler
		h.SetProfiler(profiler)
		logger.Info("user profile summarizer initialized")
	}
	h.SetSearchService(searchSvc)

	// Configure LLM proxy (CLIProxyAPI)
	handlers.SetLLMProxy(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMDefaultModel)

	// Initialize LLM extractor for fine-mode native add (non-fatal if URL not set).
	// Shared between HTTP handler (sync fine add) and scheduler worker (async mem_read).
	// Uses shared llm.Client with retry + model fallback on quota errors.
	var extractor *llm.LLMExtractor
	if cfg.LLMProxyURL != "" {
		extractClient := llm.NewClient(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMExtractModel, cfg.LLMFallbackModels, logger)
		extractor = llm.NewLLMExtractorWithClient(extractClient)
		h.SetLLMExtractor(extractor)
		logger.Info("llm extractor initialized",
			slog.String("model", extractor.Model()),
			slog.String("url", cfg.LLMProxyURL),
			slog.Any("fallback_models", cfg.LLMFallbackModels),
		)
	}

	// Start scheduler Worker (after embedder is initialized).
	// The Worker uses its own consumer group (memdb_go_scheduler), independent from
	// Python's scheduler_group — both receive all messages in parallel via separate groups.
	if rd != nil {
		var reorg *scheduler.Reorganizer
		if pg != nil && cfg.LLMProxyURL != "" {
			reorgLLMClient := llm.NewClient(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMDefaultModel, cfg.LLMFallbackModels, logger)
			reorg = scheduler.NewReorganizer(
				pg, emb, wmCache,
				reorgLLMClient,
				logger,
			)
			if extractor != nil {
				reorg.SetLLMExtractor(extractor)
			}
			if profiler != nil {
				reorg.SetProfiler(profiler)
			}
			logger.Info("scheduler reorganizer initialized",
				slog.String("model", cfg.LLMDefaultModel),
				slog.Any("fallback_models", cfg.LLMFallbackModels),
			)
		} else {
			logger.Info("scheduler reorganizer disabled (postgres or LLM not configured)")
		}
		w := scheduler.NewWorker(rd.Client(), reorg, logger)
		go w.Run(ctx)
		logger.Info("scheduler worker started")
		// Share the Worker's TaskStatusTracker with the HTTP handler so that
		// /scheduler/wait and /scheduler/wait/stream read from memos:task_meta:{user_id}
		// — the same Redis Hash written by the Worker and Python's dispatcher.
		h.SetTaskTracker(scheduler.NewTaskStatusTracker(rd.Client()))
	}

	// Create router using Go 1.22+ stdlib ServeMux
	mux := http.NewServeMux()

	// ─── Health endpoints (native Go, no proxy) ─────────────────────────
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("GET /ready", h.ReadinessCheck)

	// ─── OpenAPI spec + Swagger UI ───────────────────────────────────────
	registerOpenAPIRoutes(mux)

	// ─── OpenAI-compatible embeddings (internal, no auth) ────────────────
	mux.HandleFunc("POST /v1/embeddings", h.OpenAIEmbeddings)

	// ─── Server Router Endpoints (server_router.py — deployed) ─────────

	// Memory CRUD — native or validated
	mux.HandleFunc("POST /product/get_all", h.NativeGetAll)
	mux.HandleFunc("POST /product/add", h.NativeAdd)
	mux.HandleFunc("POST /product/search", h.NativeSearch)

	// Chat — validated
	mux.HandleFunc("POST /product/chat/complete", h.ValidatedChatComplete)
	mux.HandleFunc("POST /product/chat/stream", h.ValidatedChatStream)
	mux.HandleFunc("POST /product/chat/stream/playground", h.ProxyToProduct)

	// LLM proxy — direct CLIProxyAPI (no memory retrieval)
	mux.HandleFunc("POST /product/llm/complete", h.ProxyLLMComplete)

	// Suggestions
	mux.HandleFunc("POST /product/suggestions", h.ProxyToProduct)
	mux.HandleFunc("GET /product/suggestions/{user_id}", h.ProxyToProduct)

	// Scheduler — native Go (queries Redis Streams consumer group directly)
	mux.HandleFunc("GET /product/scheduler/allstatus", h.NativeSchedulerAllStatus)
	mux.HandleFunc("GET /product/scheduler/status", h.NativeSchedulerStatus)
	mux.HandleFunc("GET /product/scheduler/task_queue_status", h.NativeSchedulerTaskQueueStatus)
	mux.HandleFunc("POST /product/scheduler/wait", h.NativeSchedulerWait)
	mux.HandleFunc("GET /product/scheduler/wait/stream", h.NativeSchedulerWaitStream)

	// Memory (Server) — native with proxy fallback
	mux.HandleFunc("POST /product/get_memory", h.NativePostGetMemory)
	mux.HandleFunc("GET /product/get_memory/{memory_id}", h.NativeGetMemory)
	mux.HandleFunc("POST /product/get_memory_by_ids", h.NativeGetMemoryByIDs)
	mux.HandleFunc("POST /product/delete_memory", h.NativeDelete)

	// Feedback — validated
	mux.HandleFunc("POST /product/feedback", h.ValidatedFeedback)

	// Internal — native with proxy fallback
	mux.HandleFunc("POST /product/get_user_names_by_memory_ids", h.NativeGetUserNamesByMemoryIDs)
	mux.HandleFunc("POST /product/exist_mem_cube_id", h.NativeExistMemCube)

	// ─── Product Router Endpoints (product_router.py — migration) ───────

	// Configuration — native stubs with proxy fallback
	mux.HandleFunc("POST /product/configure", h.NativeConfigure)
	mux.HandleFunc("GET /product/configure/{user_id}", h.NativeGetConfig)

	// User management — native with proxy fallback
	mux.HandleFunc("POST /product/users/register", h.NativeRegisterUser)
	mux.HandleFunc("GET /product/users", h.NativeListUsers)
	mux.HandleFunc("GET /product/users/{user_id}", h.NativeGetUser)
	mux.HandleFunc("GET /product/users/{user_id}/config", h.NativeGetUserConfig)
	mux.HandleFunc("PUT /product/users/{user_id}/config", h.NativeUpdateUserConfig)

	// Chat (product_router variant — SSE streaming)
	mux.HandleFunc("POST /product/chat", h.ValidatedChatStream)

	// Instance monitoring — native
	mux.HandleFunc("GET /product/instances/status", h.NativeInstancesStatus)
	mux.HandleFunc("GET /product/instances/count", h.NativeInstancesCount)

	// ─── Apply middleware stack ──────────────────────────────────────────
	// Order: outermost wrapper first → innermost last
	var handler http.Handler = mux
	handler = mw.Cache(logger, mw.CacheConfig{Client: cacheClient})(handler)
	handler = mw.OTel(logger, cfg.OTelEnabled)(handler)
	handler = mw.RateLimit(logger, mw.RateLimitConfig{
		Enabled:       cfg.RateLimitEnabled,
		RPS:           cfg.RateLimitRPS,
		Burst:         cfg.RateLimitBurst,
		ServiceSecret: cfg.InternalServiceSecret,
	})(handler)
	handler = mw.Auth(logger, mw.AuthConfig{
		Enabled:       cfg.AuthEnabled,
		MasterKeyHash: cfg.MasterKeyHash,
		ServiceSecret: cfg.InternalServiceSecret,
	})(handler)
	handler = mw.CORS(handler)
	handler = mw.Logging(logger)(handler)
	handler = mw.RequestID(handler)
	handler = mw.Recovery(logger)(handler)

	srv := &http.Server{
		Addr:         ":" + cfg.PortStr(),
		Handler:      handler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	cleanup := func() {
		h.Close()
		if cacheClient != nil {
			cacheClient.Close()
		}
	}

	return srv, cleanup
}
