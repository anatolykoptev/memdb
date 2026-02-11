// Package handlers implements HTTP handlers for the MemDB Go API.
// Phase 1: All endpoints proxy to the Python backend.
// Phase 2+: Handlers will be replaced with native Go implementations.
package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/embedder"
	"github.com/MemDBai/MemDB/memdb-go/internal/rpc"
	"github.com/MemDBai/MemDB/memdb-go/internal/search"
)

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	python        *rpc.PythonClient
	logger        *slog.Logger
	postgres      *db.Postgres          // nil = not initialized, fall back to proxy
	qdrant        *db.Qdrant            // nil = not initialized
	redis         *db.Redis             // nil = not initialized
	embedder      embedder.Embedder     // nil = native search disabled
	searchService *search.SearchService // nil = search falls back to proxy
}

// NewHandler creates a new Handler with the given dependencies.
func NewHandler(python *rpc.PythonClient, logger *slog.Logger) *Handler {
	return &Handler{
		python: python,
		logger: logger,
	}
}

// SetDBClients sets optional database clients for native handlers.
// When set, supported endpoints use direct DB access instead of proxying.
func (h *Handler) SetDBClients(pg *db.Postgres, qd *db.Qdrant, rd *db.Redis) {
	h.postgres = pg
	h.qdrant = qd
	h.redis = rd
}

// SetEmbedder sets the embedding client for native search.
func (h *Handler) SetEmbedder(e embedder.Embedder) {
	h.embedder = e
}

// SetSearchService sets the unified search service for native search handlers.
func (h *Handler) SetSearchService(svc *search.SearchService) {
	h.searchService = svc
}

// Close releases all database connections and resources held by the handler.
func (h *Handler) Close() {
	if h.embedder != nil {
		if err := h.embedder.Close(); err != nil {
			h.logger.Error("embedder close error", slog.Any("error", err))
		} else {
			h.logger.Info("embedder closed")
		}
	}
	if h.postgres != nil {
		h.postgres.Close()
		h.logger.Info("postgres connection closed")
	}
	if h.qdrant != nil {
		if err := h.qdrant.Close(); err != nil {
			h.logger.Error("qdrant close error", slog.Any("error", err))
		} else {
			h.logger.Info("qdrant connection closed")
		}
	}
	if h.redis != nil {
		if err := h.redis.Close(); err != nil {
			h.logger.Error("redis close error", slog.Any("error", err))
		} else {
			h.logger.Info("redis connection closed")
		}
	}
}

// --- Health endpoints (handled directly by Go, no proxy) ---

// Health returns a simple health check response.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"service":    "memdb-go",
			"status":     "healthy",
			"go_version": runtime.Version(),
			"hostname":   hostname,
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		},
	})
}

// ReadinessCheck verifies that the Python backend and all configured databases are reachable.
func (h *Handler) ReadinessCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	checks := map[string]string{}

	// Python backend
	if err := h.python.HealthCheck(ctx); err != nil {
		checks["python"] = err.Error()
	} else {
		checks["python"] = "ok"
	}

	// Native DB clients (only check if initialized)
	if h.postgres != nil {
		if err := h.postgres.Ping(ctx); err != nil {
			checks["postgres"] = err.Error()
		} else {
			checks["postgres"] = "ok"
		}
	}
	if h.qdrant != nil {
		if err := h.qdrant.Ping(ctx); err != nil {
			checks["qdrant"] = err.Error()
		} else {
			checks["qdrant"] = "ok"
		}
	}
	if h.redis != nil {
		if err := h.redis.Ping(ctx); err != nil {
			checks["redis"] = err.Error()
		} else {
			checks["redis"] = "ok"
		}
	}

	// If Python is down, report 503
	if checks["python"] != "ok" {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "backend unavailable",
			"data":    checks,
		})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ready",
		"data":    checks,
	})
}

// --- Proxy endpoints (Phase 1: forward everything to Python) ---

// ProxyToProduct proxies requests to the Python /product/* endpoints.
func (h *Handler) ProxyToProduct(w http.ResponseWriter, r *http.Request) {
	h.python.ProxyRequest(r.Context(), w, r)
}

// --- Cache helpers ---

// cachePrefix is the key namespace for DB-level cache (distinct from middleware's memdb:cache:).
const cachePrefix = "memdb:db:"

// cacheGet reads a value from Redis. Returns nil on miss, error, or if redis is nil.
func (h *Handler) cacheGet(ctx context.Context, key string) []byte {
	if h.redis == nil {
		return nil
	}
	val, err := h.redis.Client().Get(ctx, key).Bytes()
	if err != nil {
		// redis.Nil is a normal cache miss; other errors are debug-logged
		if err.Error() != "redis: nil" {
			h.logger.Debug("cache get error", slog.String("key", key), slog.Any("error", err))
		}
		return nil
	}
	return val
}

// cacheSet stores a value with TTL. No-op if redis is nil. Errors are debug-logged.
func (h *Handler) cacheSet(ctx context.Context, key string, value []byte, ttl time.Duration) {
	if h.redis == nil {
		return
	}
	if err := h.redis.Client().Set(ctx, key, value, ttl).Err(); err != nil {
		h.logger.Debug("cache set error", slog.String("key", key), slog.Any("error", err))
	}
}

// cacheInvalidate deletes keys matching the given patterns. Uses SCAN (production-safe).
// No-op if redis is nil. Errors are debug-logged.
func (h *Handler) cacheInvalidate(ctx context.Context, patterns ...string) {
	if h.redis == nil {
		return
	}
	client := h.redis.Client()
	for _, pattern := range patterns {
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
			if err != nil {
				h.logger.Debug("cache scan error", slog.String("pattern", pattern), slog.Any("error", err))
				break
			}
			if len(keys) > 0 {
				if err := client.Del(ctx, keys...).Err(); err != nil {
					h.logger.Debug("cache del error", slog.Any("error", err))
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
}

// --- Helpers ---

func (h *Handler) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		h.logger.Error("failed to encode JSON response", slog.Any("error", err))
	}
}
