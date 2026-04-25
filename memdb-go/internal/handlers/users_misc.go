package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"
)

// NativeInstancesStatus handles GET /product/instances/status.
func (h *Handler) NativeInstancesStatus(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{
			"status":     "running",
			"hostname":   hostname,
			"go_version": runtime.Version(),
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		},
	})
}

// NativeInstancesCount handles GET /product/instances/count natively via PostgreSQL.
func (h *Handler) NativeInstancesCount(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
		return
	}

	ctx := r.Context()
	cacheKey := cachePrefix + "users:count"

	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached) //nolint:errcheck
		return
	}

	count, err := h.postgres.CountDistinctUsers(ctx)
	if err != nil {
		h.logger.Debug("native instances_count failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
		return
	}

	resp := map[string]any{
		"code": 200, "message": "ok",
		"data": map[string]any{"count": count},
	}
	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, usersCacheTTL)
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// NativeExistMemCube handles POST /product/exist_mem_cube_id natively via PostgreSQL.
func (h *Handler) NativeExistMemCube(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ValidatedExistMemCube(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req existMemCubeRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if req.MemCubeID == nil || *req.MemCubeID == "" {
		h.writeValidationError(w, []string{"mem_cube_id is required"})
		return
	}

	exists, err := h.postgres.ExistUser(r.Context(), *req.MemCubeID)
	if err != nil {
		h.logger.Debug("native exist_mem_cube_id failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{"code": 200, "message": "ok", "data": exists})
}

// getUserNamesByMemoryIDsRequest validates POST /product/get_user_names_by_memory_ids.
type getUserNamesByMemoryIDsRequest struct {
	MemoryIDs *[]string `json:"memory_ids"`
}

// NativeGetUserNamesByMemoryIDs handles POST /product/get_user_names_by_memory_ids natively.
func (h *Handler) NativeGetUserNamesByMemoryIDs(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req getUserNamesByMemoryIDsRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if req.MemoryIDs == nil || len(*req.MemoryIDs) == 0 {
		h.writeValidationError(w, []string{"memory_ids is required and must be non-empty"})
		return
	}

	result, err := h.postgres.GetUserNamesByMemoryIDs(r.Context(), *req.MemoryIDs)
	if err != nil {
		h.logger.Debug("native get_user_names_by_memory_ids failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{"code": 200, "message": "ok", "data": result})
}

// NativeListCubesByTag handles GET /product/cubes?tag=<tag> natively.
func (h *Handler) NativeListCubesByTag(w http.ResponseWriter, r *http.Request) {
	tag := r.URL.Query().Get("tag")
	if tag == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": 400, "message": "tag query parameter is required", "data": nil,
		})
		return
	}

	if h.postgres == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code": 503, "message": "postgres unavailable", "data": nil,
		})
		return
	}

	ctx := r.Context()
	cacheKey := cachePrefix + "cubes:tag:" + tag
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached) //nolint:errcheck
		return
	}

	cubes, err := h.postgres.ListCubesByTag(ctx, tag)
	if err != nil {
		h.logger.Error("list cubes by tag failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 500, "message": "list cubes failed: " + err.Error(), "data": nil,
		})
		return
	}
	if cubes == nil {
		cubes = []string{}
	}

	resp := map[string]any{"code": 200, "message": "ok", "data": cubes}
	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, usersCacheTTL)
	}
	h.writeJSON(w, http.StatusOK, resp)
}
