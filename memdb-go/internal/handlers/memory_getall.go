package handlers

// memory_getall.go — native GET-all handler: paginated listing of memories by type.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// getAllNativeRequest extends getAllRequest with pagination fields.
type getAllNativeRequest struct {
	UserID     *string `json:"user_id"`
	MemoryType *string `json:"memory_type"`
	Page       *int    `json:"page,omitempty"`
	PageSize   *int    `json:"page_size,omitempty"`
}

// memoryTypeToDBType maps API memory_type values to DB memory_type values.
var memoryTypeToDBType = map[string]string{
	"text_mem":  "LongTermMemory",
	"act_mem":   "ActivationMemory",
	"param_mem": "ParametricMemory",
	"para_mem":  "ParametricMemory",
	"skill_mem": "SkillMemory",
	"user_mem":  "UserMemory",
	"pref_mem":  "PreferenceMemory",
}

// maxPageSize caps the page_size parameter to prevent excessive DB load.
const maxPageSize = 1000

// NativeGetAll handles POST /product/get_all natively via PostgreSQL.
// Falls back to Python proxy if Postgres is not initialized.
func (h *Handler) NativeGetAll(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ValidatedGetAll(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req getAllNativeRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	errs := validateGetAllRequest(req.UserID, req.MemoryType)
	if !h.checkErrors(w, errs) {
		return
	}

	page := 0
	pageSize := 100
	if req.Page != nil && *req.Page >= 0 {
		page = *req.Page
	}
	if req.PageSize != nil && *req.PageSize > 0 {
		pageSize = *req.PageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	dbType := memoryTypeToDBType[*req.MemoryType]
	ctx := r.Context()
	cacheKey := fmt.Sprintf("%sget_all:%s:%s:%d:%d", cachePrefix, *req.UserID, *req.MemoryType, page, pageSize)

	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	results, total, err := h.postgres.GetAllMemories(ctx, *req.UserID, dbType, page, pageSize)
	if err != nil {
		h.logger.Debug("native get_all failed, falling back to proxy",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, body)
		return
	}

	memories := make([]map[string]any, 0, len(results))
	for _, result := range results {
		entry := map[string]any{"memory_id": result["memory_id"]}
		if propsStr, ok := result["properties"].(string); ok {
			var props map[string]any
			if json.Unmarshal([]byte(propsStr), &props) == nil {
				entry["properties"] = props
			} else {
				entry["properties"] = propsStr
			}
		}
		memories = append(memories, entry)
	}

	resp := map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"memories": memories,
			"total":    total,
		},
	}
	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, 30*time.Second)
	}

	h.writeJSON(w, http.StatusOK, resp)
}
