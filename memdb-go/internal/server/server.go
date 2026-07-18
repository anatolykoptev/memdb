// Package server sets up the HTTP server with all routes and middleware.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/cache"
	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/handlers"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"github.com/anatolykoptev/memdb/memdb-go/internal/rpc"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

// New creates a fully configured HTTP server and returns a cleanup function
// that closes all database connections on shutdown.
// ctx is used to control the lifetime of background workers (scheduler).
func New(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*http.Server, func()) {
	// Pre-warm every per-package metric singleton so /metrics exposes the
	// full series space (with pre-registered zero values) before the first
	// request lands. Without this, lazy sync.Once accessors leave Grafana
	// panels blank and alert rules in "no data" until traffic naturally
	// exercises each code path.
	handlers.PrewarmMetrics()
	search.PrewarmMetrics()
	observability.PrewarmMetrics()

	// Initialize cache client (non-fatal if unavailable)
	var cacheClient *cache.Client
	if cfg.CacheEnabled {
		var err error
		cacheClient, err = cache.New(cfg.RedisURL, logger)
		if err != nil {
			logger.Warn("cache disabled: redis unavailable", slog.Any("error", err))
		}
	}

	// Initialize Python proxy client and base handler
	h := handlers.NewHandler(rpc.NewPythonClient(cfg.PythonBackendURL, logger), logger)
	h.SetConfig(cfg)

	// Initialize database clients for Phase 2 native handlers (non-fatal)
	pg, qd, rd, wmCache := initDBClients(ctx, cfg, h, logger)

	// Initialize embedder via factory (non-fatal: server starts without embedder)
	emb := initEmbedder(cfg, h, cacheClient, logger)

	// Wire the atomic-extract LLM-call cache. Same cacheClient backs the embed
	// cache (so no additional Redis connection); when nil (cache disabled),
	// SetAtomicExtractCache becomes a no-op and extract always hits the LLM.
	h.SetAtomicExtractCache(cacheClient, 0)

	// Wire SPLADE sparse embedder (hybrid retrieval: dense + sparse).
	// Requires the embed-server SPLADE model loaded (already true in prod
	// via EMBED_MODELS env). When EmbedURL is empty (proxy-only mode) we
	// skip — sparse_embedding column stays NULL and retrieval degrades
	// gracefully to dense-only.
	if cfg.EmbedURL != "" {
		h.SetSparseEmbedder(embedder.NewSparseEmbedder(cfg.EmbedURL, "", logger))
		logger.Info("sparse embedder wired (SPLADE-v3-distilbert via embed-server)")
	}

	// Initialize search service with optional LLM features and profiler
	searchSvc, profiler := initSearchService(cfg, pg, qd, emb, rd, h, cacheClient, logger)
	h.SetSearchService(searchSvc)

	// Initialize LLM extractor (chat client with LLM API config is wired below).
	extractor := initLLMExtractor(cfg, h, logger)

	// Initialize chat LLM client (reuses LLM API config, same default model)
	var chatClient *llm.Client
	if cfg.LLMProxyURL != "" {
		chatClient = llm.NewClient(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMDefaultModel, cfg.LLMFallbackModels, logger)
		h.SetChatLLM(chatClient)
		logger.Info("chat LLM client initialized", slog.String("model", cfg.LLMDefaultModel))
	}

	// Configure buffer zone (batches default-mode adds before LLM extraction)
	if cfg.BufferEnabled {
		h.SetBufferConfig(handlers.BufferConfig{
			Enabled: true,
			Size:    cfg.BufferSize,
			TTL:     cfg.BufferTTL,
		})
		go h.StartBufferFlusher(ctx)
		logger.Info("buffer zone enabled",
			slog.Int("size", cfg.BufferSize),
			slog.Duration("ttl", cfg.BufferTTL))
	}

	// Configure ingestion queue (bounded concurrency for /product/add)
	if cfg.AddWorkers > 0 {
		h.SetAddQueue(cfg.AddWorkers, cfg.AddQueueSize)
		logger.Info("add ingestion queue enabled",
			slog.Int("workers", cfg.AddWorkers),
			slog.Int("queue_size", cfg.AddQueueSize))
	}

	// Start scheduler Worker (after embedder is initialized).
	if rd != nil {
		reorg := initReorganizer(ctx, cfg, pg, rd, emb, wmCache, extractor, profiler, logger)
		w := scheduler.NewWorker(rd.Client(), reorg, logger)
		// M10 Stream 7: wire Postgres for PageRank background goroutine.
		// SetPostgres is a no-op when pg is nil, so no extra nil guard needed.
		w.SetPostgres(pg)
		// M11 F11: wire chat LLM as the bi-temporal validator's judge.
		// Loop is gated by MEMDB_F11_VALIDATOR_ENABLED so a nil-safe wire here
		// is harmless when the feature flag is off.
		if chatClient != nil {
			w.SetLLMJudge(chatClient)
		}
		go w.Run(ctx)
		if reorg != nil {
			h.SetReorganizer(reorg)
		}
		tracker := scheduler.NewTaskStatusTracker(rd.Client())
		h.SetTaskTracker(tracker)
		// PF-9 (#327): start task FSM watchdog to reclaim stuck tasks.
		staleTimeout := time.Duration(envcfg.IntRange("MEMDB_TASK_STALE_TIMEOUT_M", 30, 1, 1<<30)) * time.Minute
		go tracker.RunWatchdog(ctx, 5*time.Minute, staleTimeout)
		logger.Info("task watchdog started",
			slog.Duration("stale_timeout", staleTimeout),
			slog.Duration("scan_interval", 5*time.Minute))
		logger.Info("scheduler worker started")
		// Surface any MEMDB_D3_* / search tuning overrides that were picked
		// up from .env — invisible otherwise and a common source of "why is
		// my threshold still the default" confusion during D3 tuning sweeps.
		scheduler.LogTuningOverrides(logger)
	}

	// Create router and apply middleware
	mux := http.NewServeMux()
	registerRoutes(mux, h, cfg.InternalServiceSecret)

	srv := &http.Server{
		Addr:         ":" + cfg.PortStr(),
		Handler:      applyMiddleware(mux, cfg, cacheClient, logger),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	return srv, func() {
		h.Close()
		if cacheClient != nil {
			cacheClient.Close()
		}
	}
}
