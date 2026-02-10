package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/search"
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

	ctx := r.Context()
	cacheKey := cachePrefix + "memory:" + memoryID

	// Check cache
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	result, err := h.postgres.GetMemoryByID(ctx, memoryID)
	if err != nil {
		h.logger.Debug("native get_memory failed, falling back to proxy",
			slog.String("memory_id", memoryID),
			slog.Any("error", err),
		)
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

	// Parse properties JSON string into a map
	if propsStr, ok := result["properties"].(string); ok {
		var props map[string]any
		if json.Unmarshal([]byte(propsStr), &props) == nil {
			result["properties"] = props
		}
	}

	resp := map[string]any{
		"code":    200,
		"message": "ok",
		"data":    result,
	}
	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, 120*time.Second)
	}

	h.writeJSON(w, http.StatusOK, resp)
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

	ctx := r.Context()
	ids := *req.MemoryIDs

	// Check per-ID cache, collect misses
	cached := make(map[string][]byte, len(ids))
	var missingIDs []string
	for _, id := range ids {
		key := cachePrefix + "memory:" + id
		if val := h.cacheGet(ctx, key); val != nil {
			cached[id] = val
		} else {
			missingIDs = append(missingIDs, id)
		}
	}

	// Fetch missing from DB
	var dbResults []map[string]any
	if len(missingIDs) > 0 {
		var err error
		dbResults, err = h.postgres.GetMemoryByIDs(ctx, missingIDs)
		if err != nil {
			h.logger.Debug("native get_memory_by_ids failed, falling back to proxy",
				slog.Any("error", err),
			)
			h.proxyWithBody(w, r, body)
			return
		}
	}

	// Build result: merge cached + fresh, preserving request order
	dbByID := make(map[string]map[string]any, len(dbResults))
	for _, result := range dbResults {
		entry := map[string]any{"memory_id": result["memory_id"]}
		if propsStr, ok := result["properties"].(string); ok {
			var props map[string]any
			if json.Unmarshal([]byte(propsStr), &props) == nil {
				entry["properties"] = props
			} else {
				entry["properties"] = propsStr
			}
		}
		if mid, ok := result["memory_id"].(string); ok {
			dbByID[mid] = entry
			// Cache individual results
			resp := map[string]any{"code": 200, "message": "ok", "data": entry}
			if encoded, err := json.Marshal(resp); err == nil {
				h.cacheSet(ctx, cachePrefix+"memory:"+mid, encoded, 120*time.Second)
			}
		}
	}

	parsed := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if raw, ok := cached[id]; ok {
			// Decode cached single-memory response to extract data
			var resp map[string]any
			if json.Unmarshal(raw, &resp) == nil {
				if data, ok := resp["data"].(map[string]any); ok {
					parsed = append(parsed, data)
					continue
				}
			}
		}
		if entry, ok := dbByID[id]; ok {
			parsed = append(parsed, entry)
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    parsed,
	})
}

// prefCollectionsGetMemory lists the Qdrant collections for preference memory.
var prefCollectionsGetMemory = []string{"explicit_preference", "implicit_preference"}

// NativePostGetMemory handles POST /product/get_memory natively via PostgreSQL+Qdrant.
// Falls back to Python proxy if Postgres is not initialized or if complex filters are used.
//
// This returns all memory types in a single response:
//   - text_mem (LongTermMemory) from PolarDB
//   - skill_mem (SkillMemory) from PolarDB
//   - pref_mem from Qdrant preference collections
//   - tool_mem (always empty)
func (h *Handler) NativePostGetMemory(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.ValidatedGetMemory(w, r)
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req getMemoryRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	// mem_cube_id is required
	if req.MemCubeID == nil || *req.MemCubeID == "" {
		h.writeValidationError(w, []string{"mem_cube_id is required"})
		return
	}

	// Complex filters: fall back to Python
	if len(req.Filter) > 0 {
		h.logger.Debug("native post_get_memory: filter specified, proxying")
		h.proxyWithBody(w, r, body)
		return
	}

	// Defaults
	memCubeID := *req.MemCubeID
	page := 0
	pageSize := 100
	includePreference := true
	includeSkillMemory := true

	if req.Page != nil && *req.Page >= 0 {
		page = *req.Page
	}
	if req.PageSize != nil && *req.PageSize > 0 {
		pageSize = *req.PageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	if req.IncludePreference != nil {
		includePreference = *req.IncludePreference
	}
	if req.IncludeSkillMemory != nil {
		includeSkillMemory = *req.IncludeSkillMemory
	}

	ctx := r.Context()
	cacheKey := fmt.Sprintf("%spost_get_memory:%s:%d:%d:%t:%t",
		cachePrefix, memCubeID, page, pageSize, includePreference, includeSkillMemory)

	// Check cache
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	// Fetch text_mem (LongTermMemory)
	textResults, textTotal, err := h.postgres.GetAllMemories(ctx, memCubeID, "LongTermMemory", page, pageSize)
	if err != nil {
		h.logger.Debug("native post_get_memory: text_mem query failed, proxying",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, body)
		return
	}

	textMem := formatMemoryBucket(textResults, memCubeID, textTotal)

	// Fetch skill_mem (SkillMemory) if requested
	var skillMem search.MemoryBucket
	if includeSkillMemory {
		skillResults, skillTotal, err := h.postgres.GetAllMemories(ctx, memCubeID, "SkillMemory", page, pageSize)
		if err != nil {
			h.logger.Debug("native post_get_memory: skill_mem query failed",
				slog.Any("error", err),
			)
			// Non-fatal: return empty skill_mem
			skillMem = search.MemoryBucket{
				CubeID:     memCubeID,
				Memories:   []map[string]any{},
				TotalNodes: 0,
			}
		} else {
			skillMem = formatMemoryBucket(skillResults, memCubeID, skillTotal)
		}
	} else {
		skillMem = search.MemoryBucket{
			CubeID:     memCubeID,
			Memories:   []map[string]any{},
			TotalNodes: 0,
		}
	}

	// Fetch pref_mem from Qdrant if requested and available
	var prefMem search.MemoryBucket
	if includePreference && h.qdrant != nil {
		prefItems := h.scrollPreferences(ctx, memCubeID, pageSize)
		prefMem = search.MemoryBucket{
			CubeID:     memCubeID,
			Memories:   prefItems,
			TotalNodes: len(prefItems),
		}
	} else {
		prefMem = search.MemoryBucket{
			CubeID:     memCubeID,
			Memories:   []map[string]any{},
			TotalNodes: 0,
		}
	}

	// tool_mem is always empty
	toolMem := search.MemoryBucket{
		CubeID:     memCubeID,
		Memories:   []map[string]any{},
		TotalNodes: 0,
	}

	resp := map[string]any{
		"code":    200,
		"message": "Memories retrieved successfully",
		"data": map[string]any{
			"text_mem":  []search.MemoryBucket{textMem},
			"skill_mem": []search.MemoryBucket{skillMem},
			"pref_mem":  []search.MemoryBucket{prefMem},
			"tool_mem":  []search.MemoryBucket{toolMem},
		},
	}

	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, 30*time.Second)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(encoded)
	} else {
		h.writeJSON(w, http.StatusOK, resp)
	}

	h.logger.Info("native post_get_memory complete",
		slog.String("mem_cube_id", memCubeID),
		slog.Int("text_mem", textMem.TotalNodes),
		slog.Int("skill_mem", skillMem.TotalNodes),
		slog.Int("pref_mem", prefMem.TotalNodes),
	)
}

// formatMemoryBucket formats PolarDB results into a MemoryBucket with parsed properties.
// Each memory item gets the full FormatMemoryItem treatment matching the Python API.
func formatMemoryBucket(results []map[string]any, cubeID string, total int) search.MemoryBucket {
	memories := make([]map[string]any, 0, len(results))
	for _, result := range results {
		if propsStr, ok := result["properties"].(string); ok {
			var props map[string]any
			if json.Unmarshal([]byte(propsStr), &props) == nil {
				item := search.FormatMemoryItem(props, false)
				memories = append(memories, item)
			}
		}
	}
	return search.MemoryBucket{
		CubeID:     cubeID,
		Memories:   memories,
		TotalNodes: total,
	}
}

// scrollPreferences scrolls Qdrant preference collections for a user and formats results.
func (h *Handler) scrollPreferences(ctx context.Context, userID string, limit int) []map[string]any {
	var allItems []map[string]any
	seen := make(map[string]bool)

	for _, coll := range prefCollectionsGetMemory {
		results, err := h.qdrant.ScrollByUserID(ctx, coll, userID, limit)
		if err != nil {
			h.logger.Debug("pref scroll failed",
				slog.String("collection", coll),
				slog.Any("error", err),
			)
			continue
		}

		for _, r := range results {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true

			memory, _ := r.Payload["memory"].(string)
			if memory == "" {
				memory, _ = r.Payload["memory_content"].(string)
			}
			if memory == "" {
				continue
			}

			// Build metadata from payload
			metadata := make(map[string]any)
			for k, v := range r.Payload {
				metadata[k] = v
			}
			metadata["embedding"] = []any{}
			metadata["usage"] = []any{}
			metadata["id"] = r.ID
			metadata["memory"] = memory

			refID := r.ID
			if idx := strings.IndexByte(refID, '-'); idx > 0 {
				refID = refID[:idx]
			}
			refIDStr := "[" + refID + "]"
			metadata["ref_id"] = refIDStr

			item := map[string]any{
				"id":       r.ID,
				"ref_id":   refIDStr,
				"memory":   memory,
				"metadata": metadata,
			}
			allItems = append(allItems, item)
		}
	}

	if allItems == nil {
		allItems = []map[string]any{}
	}
	return allItems
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

	// Validate required fields
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

	// Check cache
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

	// Native path: delete by memory_ids — requires user_id
	if req.UserID == nil || *req.UserID == "" {
		// Fall back to proxy when user_id is missing (Python can handle it)
		h.proxyWithBody(w, r, body)
		return
	}

	ctx := r.Context()
	deleted, err := h.postgres.DeleteByPropertyIDs(ctx, *req.MemoryIDs, *req.UserID)
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

	// Invalidate caches: get_all pages for this user, users list/count, individual memories
	h.cacheInvalidate(ctx,
		cachePrefix+"get_all:"+*req.UserID+":*",
		cachePrefix+"users:*",
	)
	for _, id := range *req.MemoryIDs {
		h.cacheInvalidate(ctx, cachePrefix+"memory:"+id)
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data": map[string]any{
			"deleted_count": deleted,
		},
	})
}
