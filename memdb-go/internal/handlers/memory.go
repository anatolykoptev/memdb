package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/MemDBai/MemDB/memdb-go/internal/db/queries"
)

// NativeGetMemory handles GET /product/get_memory/{memory_id} natively via PostgreSQL.
// Falls back to Python proxy if the Postgres client is not initialized.
func (h *Handler) NativeGetMemory(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ProxyToProduct(w, r)
		return
	}

	memoryID := r.PathValue("memory_id")
	if memoryID == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{
			"code":    400,
			"message": "memory_id is required",
			"data":    nil,
		})
		return
	}

	result, err := h.postgres.GetMemoryByID(r.Context(), memoryID)
	if err != nil {
		h.logger.Debug("native get_memory failed, falling back to proxy",
			slog.String("memory_id", memoryID),
			slog.Any("error", err),
		)
		// Fall back to proxy on any DB error
		h.ProxyToProduct(w, r)
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

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    result,
	})
}

// NativeGetMemoryByIDs handles POST /product/get_memory_by_ids natively via PostgreSQL.
// Falls back to Python proxy if the Postgres client is not initialized.
func (h *Handler) NativeGetMemoryByIDs(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ValidatedGetMemoryByIDs(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req getMemoryByIDsRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if req.MemoryIDs == nil || len(*req.MemoryIDs) == 0 {
		h.writeValidationError(w, []string{"memory_ids is required and must be non-empty"})
		return
	}

	results, err := h.postgres.GetMemoryByIDs(r.Context(), *req.MemoryIDs)
	if err != nil {
		h.logger.Debug("native get_memory_by_ids failed, falling back to proxy",
			slog.Any("error", err),
		)
		// Fall back to proxy — restore body
		h.proxyWithBody(w, r, body)
		return
	}

	// Parse properties JSON for each result
	parsed := make([]map[string]any, 0, len(results))
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
		parsed = append(parsed, entry)
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    parsed,
	})
}

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
}

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

	// Validate required fields
	var errs []string
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.MemoryType == nil || *req.MemoryType == "" {
		errs = append(errs, "memory_type is required")
	} else {
		if _, ok := memoryTypeToDBType[*req.MemoryType]; !ok {
			errs = append(errs, "memory_type must be one of: text_mem, act_mem, param_mem, para_mem")
		}
	}
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

	dbType := memoryTypeToDBType[*req.MemoryType]

	results, total, err := h.postgres.GetAllMemories(r.Context(), queries.DefaultGraphName, *req.UserID, dbType, page, pageSize)
	if err != nil {
		h.logger.Debug("native get_all failed, falling back to proxy",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, body)
		return
	}

	// Parse properties JSON for each result
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

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"memories": memories,
			"total":    total,
		},
	})
}

// deleteNativeRequest extends deleteRequest with user_id for native handling.
type deleteNativeRequest struct {
	UserID    *string                `json:"user_id"`
	MemoryIDs *[]string              `json:"memory_ids"`
	FileIDs   *[]string              `json:"file_ids"`
	Filter    map[string]interface{} `json:"filter"`
}

// NativeDelete handles POST /product/delete_memory natively via PostgreSQL+Qdrant.
// Falls back to Python proxy if Postgres is not initialized or for complex filters/file_ids.
func (h *Handler) NativeDelete(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ValidatedDelete(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req deleteNativeRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	hasMemoryIDs := req.MemoryIDs != nil && len(*req.MemoryIDs) > 0
	hasFileIDs := req.FileIDs != nil && len(*req.FileIDs) > 0
	hasFilter := len(req.Filter) > 0

	if !hasMemoryIDs && !hasFileIDs && !hasFilter {
		h.writeValidationError(w, []string{"at least one of memory_ids, file_ids, or filter is required"})
		return
	}

	// Complex cases: fall back to proxy
	if hasFileIDs || hasFilter {
		h.proxyWithBody(w, r, body)
		return
	}

	// Native path: delete by memory_ids
	userName := ""
	if req.UserID != nil {
		userName = *req.UserID
	}

	deleted, err := h.postgres.DeleteByPropertyIDs(r.Context(), queries.DefaultGraphName, *req.MemoryIDs, userName)
	if err != nil {
		h.logger.Debug("native delete failed, falling back to proxy",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, body)
		return
	}

	// Also delete from Qdrant preference collections (best-effort)
	if h.qdrant != nil {
		for _, collection := range []string{"explicit_preference", "implicit_preference"} {
			if err := h.qdrant.DeleteByIDs(r.Context(), collection, *req.MemoryIDs); err != nil {
				h.logger.Debug("qdrant delete from preference collection failed",
					slog.String("collection", collection),
					slog.Any("error", err),
				)
			}
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"deleted_count": deleted,
		},
	})
}
