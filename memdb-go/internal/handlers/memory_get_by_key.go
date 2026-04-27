package handlers

// memory_get_by_key.go — POST /product/get_memory_by_key.
// Anthropic-memory-tool adapter Phase 1: address a single memory by
// (cube_id, user_id, key). Mirrors NativeGetMemory's response shape so
// downstream parsers can be reused unchanged.

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// getMemoryByKeyRequest validates POST /product/get_memory_by_key.
// All fields required: cube_id non-empty, user_id non-empty, key non-empty
// and ≤ 512 chars (validateKey).
type getMemoryByKeyRequest struct {
	CubeID *string `json:"cube_id"`
	UserID *string `json:"user_id"`
	Key    *string `json:"key"`
}

// NativeGetMemoryByKey returns the single activated memory for
// (cube_id, user_id, key) or 404 when absent. 400 on validation errors,
// 503 when Postgres is offline.
func (h *Handler) NativeGetMemoryByKey(w http.ResponseWriter, r *http.Request) {
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

	var req getMemoryByKeyRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	var errs []string
	if req.CubeID == nil || *req.CubeID == "" {
		errs = append(errs, "cube_id is required")
	}
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.Key == nil || *req.Key == "" {
		errs = append(errs, "key is required")
	} else {
		errs = append(errs, validateKey(req.Key)...)
	}
	if !h.checkErrors(w, errs) {
		return
	}

	ctx := r.Context()
	result, err := h.postgres.GetMemoryByKey(ctx, *req.CubeID, *req.UserID, *req.Key)
	if err != nil {
		h.logger.Debug("native get_memory_by_key failed",
			slog.String("cube_id", *req.CubeID),
			slog.String("user_id", *req.UserID),
			slog.Any("error", err),
		)
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
		return
	}
	if result == nil {
		h.writeJSON(w, http.StatusNotFound, map[string]any{
			"code":    404,
			"message": "memory not found",
			"data":    nil,
		})
		return
	}
	if propsStr, ok := result["properties"].(string); ok {
		var props map[string]any
		if json.Unmarshal([]byte(propsStr), &props) == nil {
			result["properties"] = props
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    result,
	})
}
