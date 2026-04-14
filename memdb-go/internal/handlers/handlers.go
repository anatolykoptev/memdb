// Package handlers implements HTTP handlers for the MemDB Go API.
// Phase 1: All endpoints proxy to the Python backend.
// Phase 2+: Handlers will be replaced with native Go implementations.
package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/rpc"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
	"golang.org/x/sync/semaphore"
)

// BufferConfig holds buffer zone settings for batching add requests.
type BufferConfig struct {
	Enabled bool
	Size    int           // count threshold (default 5)
	TTL     time.Duration // time threshold (default 30s)
}

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	python        *rpc.PythonClient
	logger        *slog.Logger
	postgres      *db.Postgres                 // nil = not initialized, fall back to proxy
	qdrant        *db.Qdrant                   // nil = not initialized
	redis         *db.Redis                    // nil = not initialized
	wmCache       *db.WorkingMemoryCache       // nil = VSET disabled, use postgres for candidates
	embedder      embedder.Embedder            // nil = native search disabled
	embedRegistry *embedder.Registry           // nil = single-model mode (uses embedder field)
	searchService *search.SearchService        // nil = search falls back to proxy
	llmExtractor  *llm.LLMExtractor            // nil = mode=fine falls back to proxy
	llmChat       *llm.Client                  // nil = chat falls back to proxy
	profiler      *scheduler.Profiler          // nil = profile summaries disabled
	tracker       *scheduler.TaskStatusTracker // nil = fall back to stream-based status
	reorg         reorgRunner                  // nil = reorganizer not configured
	bufferCfg     BufferConfig                 // buffer zone config (zero value = disabled)
	addSem        *semaphore.Weighted          // nil = no limit on concurrent adds
	addQueueMax   int64                        // max waiters before 503
	addWaiters    atomic.Int64                 // current goroutines waiting for semaphore
	cubeStore     cubeStoreClient              // nil = cube endpoints return 503
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
	if pg != nil {
		h.cubeStore = pg
	}
}

// SetEmbedder sets the embedding client for native search.
func (h *Handler) SetEmbedder(e embedder.Embedder) {
	h.embedder = e
}

// SetEmbedRegistry sets the multi-model embedder registry for /v1/embeddings.
// When set, the embeddings handler resolves models by name from the registry.
func (h *Handler) SetEmbedRegistry(r *embedder.Registry) { h.embedRegistry = r }

// SetSearchService sets the unified search service for native search handlers.
func (h *Handler) SetSearchService(svc *search.SearchService) {
	h.searchService = svc
}

// SetLLMExtractor sets the LLM extractor for native fine-mode add.
// When set, mode=fine requests are handled natively instead of proxied to Python.
func (h *Handler) SetLLMExtractor(e *llm.LLMExtractor) {
	h.llmExtractor = e
}

// SetChatLLM sets the LLM client for native chat handlers.
func (h *Handler) SetChatLLM(c *llm.Client) { h.llmChat = c }

// SetProfiler sets the background user profile summarizer.
// When set, profile summaries are triggered after fine-mode adds and injected into search results.
func (h *Handler) SetProfiler(p *scheduler.Profiler) {
	h.profiler = p
}

// SetReorganizer sets the Memory Reorganizer for on-demand reorg via /product/admin/reorg.
func (h *Handler) SetReorganizer(r reorgRunner) { h.reorg = r }

// SetTaskTracker sets the Redis-backed task status tracker.
// When set, /scheduler/wait and /scheduler/wait/stream use memos:task_meta:{user_id}
// (same schema as Python) instead of stream-based XLen/XPending heuristics.
func (h *Handler) SetTaskTracker(t *scheduler.TaskStatusTracker) {
	h.tracker = t
}

// SetBufferConfig sets the buffer zone configuration for batching add requests.
func (h *Handler) SetBufferConfig(cfg BufferConfig) {
	h.bufferCfg = cfg
}

// SetAddQueue configures bounded concurrency for native add requests.
// workers = max concurrent processing goroutines, queueSize = max waiters before 503.
func (h *Handler) SetAddQueue(workers, queueSize int) {
	if workers <= 0 {
		return
	}
	h.addSem = semaphore.NewWeighted(int64(workers))
	h.addQueueMax = int64(queueSize)
}

// SetWorkingMemoryCache sets the Redis VSET hot cache for WorkingMemory.
// When set, fine-mode dedup candidate lookup uses VSET (HNSW in-memory) instead of pgvector.
func (h *Handler) SetWorkingMemoryCache(c *db.WorkingMemoryCache) {
	h.wmCache = c
}

// Embedder returns the configured embedder, or nil if not yet set.
// Used by server.go to pass the embedder to the scheduler Reorganizer.
func (h *Handler) Embedder() embedder.Embedder {
	return h.embedder
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

// parseJSONBody decodes the request body as JSON into dst.
func parseJSONBody(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}
