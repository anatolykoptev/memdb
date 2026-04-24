package server

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	_ "net/http/pprof" // side-effect: registers /debug/pprof/* on http.DefaultServeMux

	"github.com/anatolykoptev/memdb/memdb-go/internal/handlers"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registerRoutes mounts all HTTP handlers on the provided ServeMux.
// serviceSecret is the value of INTERNAL_SERVICE_SECRET; pprof endpoints
// require it via X-Service-Secret header.
func registerRoutes(mux *http.ServeMux, h *handlers.Handler, serviceSecret string) {
	// ─── pprof endpoints behind internal-auth — leak prevention. See M7 follow-up F3. ──
	// The net/http/pprof import (above) auto-registers handlers on http.DefaultServeMux.
	// We proxy them here from our custom mux and enforce X-Service-Secret-only access:
	// no bearer tokens, so user-facing API keys cannot access goroutine/heap dumps.
	mux.Handle("/debug/pprof/", pprofHandler(serviceSecret))

	// ─── Health endpoints (native Go, no proxy) ─────────────────────────
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("GET /ready", h.ReadinessCheck)

	// Prometheus scrape endpoint — OTel Prometheus exporter reads instruments
	// from the default registry. Gated by whatever auth middleware wraps mux.
	mux.Handle("GET /metrics", promhttp.Handler())

	// ─── OpenAPI spec + Swagger UI ───────────────────────────────────────
	registerOpenAPIRoutes(mux)

	// ─── OpenAI-compatible embeddings (internal, no auth) ────────────────
	mux.HandleFunc("POST /v1/embeddings", h.OpenAIEmbeddings)

	// ─── Server Router Endpoints (server_router.py — deployed) ─────────

	// Memory CRUD — native or validated
	mux.HandleFunc("POST /product/get_all", h.NativeGetAll)
	mux.HandleFunc("POST /product/add", h.NativeAdd)
	mux.HandleFunc("POST /product/search", h.NativeSearch)

	// Chat — native (playground removed 2026-04-18; see ROADMAP Phase 4.5 followup)
	mux.HandleFunc("POST /product/chat/complete", h.NativeChatComplete)
	mux.HandleFunc("POST /product/chat/stream", h.NativeChatStream)

	// LLM passthrough — direct LLM API (no memory retrieval)
	mux.HandleFunc("POST /product/llm/complete", h.NativeLLMComplete)

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
	mux.HandleFunc("POST /product/delete_all_memories", h.NativeDeleteAll)
	mux.HandleFunc("POST /product/update_memory", h.NativeUpdateMemory)

	// Feedback — validated
	mux.HandleFunc("POST /product/feedback", h.ValidatedFeedback)

	// Internal — native with proxy fallback
	mux.HandleFunc("POST /product/get_user_names_by_memory_ids", h.NativeGetUserNamesByMemoryIDs)
	mux.HandleFunc("POST /product/exist_mem_cube_id", h.NativeExistMemCube)

	// ─── Product Router Endpoints (product_router.py — migration) ───────

	// Configuration — native stubs with proxy fallback
	mux.HandleFunc("POST /product/configure", h.NativeConfigure)
	mux.HandleFunc("GET /product/configure/{user_id}", h.NativeGetConfig)

	// Cube management — native Go
	mux.HandleFunc("POST /product/create_cube", h.NativeCreateCube)
	mux.HandleFunc("POST /product/list_cubes", h.NativeListCubes)
	mux.HandleFunc("POST /product/delete_cube", h.NativeDeleteCube)
	mux.HandleFunc("POST /product/get_user_cubes", h.NativeGetUserCubes)

	// User management — native
	mux.HandleFunc("POST /product/get_user_info", h.NativeGetUserInfo)
	mux.HandleFunc("POST /product/users/register", h.NativeRegisterUser)
	mux.HandleFunc("GET /product/users", h.NativeListUsers)
	mux.HandleFunc("GET /product/cubes", h.NativeListCubesByTag)
	mux.HandleFunc("GET /product/users/{user_id}", h.NativeGetUser)
	mux.HandleFunc("GET /product/users/{user_id}/config", h.NativeGetUserConfig)
	mux.HandleFunc("PUT /product/users/{user_id}/config", h.NativeUpdateUserConfig)

	// Chat (product_router variant — SSE streaming)
	mux.HandleFunc("POST /product/chat", h.NativeChatStream)

	// Instance monitoring — native
	mux.HandleFunc("GET /product/instances/status", h.NativeInstancesStatus)
	mux.HandleFunc("GET /product/instances/count", h.NativeInstancesCount)

	// ─── Admin endpoints ────────────────────────────────────────────────
	mux.HandleFunc("POST /product/admin/reprocess", h.AdminReprocess)
	mux.HandleFunc("POST /product/admin/reorg", h.AdminReorg)
}

// pprofHandler returns an http.Handler that gates access to the pprof index
// (http.DefaultServeMux, populated by the net/http/pprof side-effect import)
// behind X-Service-Secret. Requests without a valid secret get 401.
// Bearer token is intentionally not accepted: pprof exposes goroutine stacks,
// heap snapshots, and source paths — internal tooling only.
func pprofHandler(serviceSecret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serviceSecret == "" {
			http.Error(w, "pprof disabled: INTERNAL_SERVICE_SECRET not configured", http.StatusServiceUnavailable)
			return
		}
		got := r.Header.Get("X-Service-Secret")
		if got == "" {
			got = r.Header.Get("X-Internal-Service")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(serviceSecret)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":401,"message":"X-Service-Secret required","data":null}`)
			return
		}
		http.DefaultServeMux.ServeHTTP(w, r)
	})
}
