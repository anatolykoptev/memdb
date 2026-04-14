package server

// server_init.go — database client initialization for native handlers.
// Covers: initDBClients (postgres, qdrant, redis, wmCache), initReorganizer.

import (
	"context"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/handlers"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
)

// initDBClients connects to databases for native handlers.
// All connections are optional — handlers fall back to proxy if nil.
// Returns postgres, qdrant, redis clients and wmCache so the caller can start
// the scheduler Worker after the embedder is initialized.
func initDBClients(ctx context.Context, cfg *config.Config, h *handlers.Handler, logger *slog.Logger) (*db.Postgres, *db.Qdrant, *db.Redis, *db.WorkingMemoryCache) {
	var pg *db.Postgres
	var qd *db.Qdrant
	var rd *db.Redis

	if cfg.PostgresURL != "" {
		var err error
		pg, err = db.NewPostgres(ctx, cfg.PostgresURL, logger)
		if err != nil {
			logger.Warn("postgres unavailable, native handlers will proxy", slog.Any("error", err))
		}
	}

	if cfg.QdrantAddr != "" {
		var err error
		qd, err = db.NewQdrant(ctx, cfg.QdrantAddr, logger)
		if err != nil {
			logger.Warn("qdrant unavailable", slog.Any("error", err))
		}
	}

	if cfg.DBRedisURL != "" {
		var err error
		rd, err = db.NewRedis(ctx, cfg.DBRedisURL, logger)
		if err != nil {
			logger.Warn("redis unavailable", slog.Any("error", err))
		}
	}

	if pg != nil || qd != nil || rd != nil {
		h.SetDBClients(pg, qd, rd)
		logger.Info("native db clients initialized",
			slog.Bool("postgres", pg != nil),
			slog.Bool("qdrant", qd != nil),
			slog.Bool("redis", rd != nil),
		)
	}

	// Initialize WorkingMemory VSET hot cache (requires Redis 8+)
	var wmCache *db.WorkingMemoryCache
	if rd != nil {
		wmCache = db.NewWorkingMemoryCache(rd)
		h.SetWorkingMemoryCache(wmCache)
		logger.Info("working memory vset cache initialized")

		// Warm VSET cache from Postgres on startup — runs in background so it
		// doesn't block server readiness. Non-fatal: errors are logged inside Sync.
		if pg != nil {
			go wmCache.Sync(ctx, pg, logger)
		}
	}

	return pg, qd, rd, wmCache
}

// initReorganizer creates and configures a Reorganizer if postgres and LLM are available.
// Returns nil when prerequisites are missing — the scheduler Worker runs without reorganization.
func initReorganizer(
	_ context.Context,
	cfg *config.Config,
	pg *db.Postgres,
	rd *db.Redis,
	emb embedder.Embedder,
	wmCache *db.WorkingMemoryCache,
	extractor *llm.LLMExtractor,
	profiler *scheduler.Profiler,
	logger *slog.Logger,
) *scheduler.Reorganizer {
	if pg == nil || cfg.LLMProxyURL == "" {
		logger.Info("scheduler reorganizer disabled (postgres or LLM not configured)")
		return nil
	}
	reorgLLMClient := llm.NewClient(cfg.LLMProxyURL, cfg.LLMProxyAPIKey, cfg.LLMReorgModel, cfg.LLMFallbackModels, logger)
	reorg := scheduler.NewReorganizer(pg, emb, wmCache, reorgLLMClient, logger)
	if extractor != nil {
		reorg.SetLLMExtractor(extractor)
	}
	if profiler != nil {
		reorg.SetProfiler(profiler)
	}
	if rd != nil {
		reorg.SetCacheInvalidator(scheduler.NewRedisCacheInvalidator(rd.Client(), logger))
	}
	if cfg.ReorgUseHNSW {
		reorg.SetUseHNSW(true)
		logger.Info("scheduler reorganizer: HNSW near-duplicate path enabled")
	}
	logger.Info("scheduler reorganizer initialized",
		slog.String("model", cfg.LLMReorgModel),
		slog.Any("fallback_models", cfg.LLMFallbackModels),
	)
	return reorg
}
