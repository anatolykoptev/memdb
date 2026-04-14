package server

import (
	"net/http"

	"github.com/anatolykoptev/memdb/memdb-go/internal/handlers"
)

// registerRoutes mounts all HTTP handlers on the provided ServeMux.
func registerRoutes(mux *http.ServeMux, h *handlers.Handler) {
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

	// Chat — native with proxy fallback (playground stays proxied)
	mux.HandleFunc("POST /product/chat/complete", h.NativeChatComplete)
	mux.HandleFunc("POST /product/chat/stream", h.NativeChatStream)
	mux.HandleFunc("POST /product/chat/stream/playground", h.ProxyToProduct)

	// LLM passthrough — direct CLIProxyAPI (no memory retrieval)
	mux.HandleFunc("POST /product/llm/complete", h.NativeLLMComplete)

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
