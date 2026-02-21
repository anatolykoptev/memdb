package handlers

// memory_get.go — native GET handlers: get_memory, get_memory_by_ids, post_get_memory.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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

	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	// Clients send property UUIDs; use GetMemoryByPropertyID not GetMemoryByID (AGE graph ID).
	result, err := h.postgres.GetMemoryByPropertyID(ctx, memoryID)
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

	// Check per-ID cache, collect misses.
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

	// Fetch missing from DB by property UUID (clients send property UUIDs, not AGE graph IDs).
	var dbResults []map[string]any
	if len(missingIDs) > 0 {
		var err error
		dbResults, err = h.postgres.GetMemoriesByPropertyIDs(ctx, missingIDs)
		if err != nil {
			h.logger.Debug("native get_memory_by_ids failed, falling back to proxy",
				slog.Any("error", err),
			)
			h.proxyWithBody(w, r, body)
			return
		}
	}

	// Build result: merge cached + fresh, preserving request order.
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
			resp := map[string]any{"code": 200, "message": "ok", "data": entry}
			if encoded, err := json.Marshal(resp); err == nil {
				h.cacheSet(ctx, cachePrefix+"memory:"+mid, encoded, 120*time.Second)
			}
		}
	}

	parsed := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if raw, ok := cached[id]; ok {
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

	if req.MemCubeID == nil || *req.MemCubeID == "" {
		h.writeValidationError(w, []string{"mem_cube_id is required"})
		return
	}

	if len(req.Filter) > 0 {
		h.logger.Debug("native post_get_memory: filter specified, proxying")
		h.proxyWithBody(w, r, body)
		return
	}

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

	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	// Fetch text_mem (LongTermMemory + UserMemory).
	textResults, textTotal, err := h.postgres.GetAllMemoriesByTypes(ctx, memCubeID, []string{"LongTermMemory", "UserMemory"}, page, pageSize)
	if err != nil {
		h.logger.Debug("native post_get_memory: text_mem query failed, proxying",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, body)
		return
	}

	textMem := formatMemoryBucket(textResults, memCubeID, textTotal)

	var skillMem search.MemoryBucket
	if includeSkillMemory {
		skillResults, skillTotal, err := h.postgres.GetAllMemories(ctx, memCubeID, "SkillMemory", page, pageSize)
		if err != nil {
			h.logger.Debug("native post_get_memory: skill_mem query failed",
				slog.Any("error", err),
			)
			skillMem = search.MemoryBucket{CubeID: memCubeID, Memories: []map[string]any{}, TotalNodes: 0}
		} else {
			skillMem = formatMemoryBucket(skillResults, memCubeID, skillTotal)
		}
	} else {
		skillMem = search.MemoryBucket{CubeID: memCubeID, Memories: []map[string]any{}, TotalNodes: 0}
	}

	var prefMem search.MemoryBucket
	if includePreference && h.qdrant != nil {
		prefItems := h.scrollPreferences(ctx, memCubeID, pageSize)
		prefMem = search.MemoryBucket{CubeID: memCubeID, Memories: prefItems, TotalNodes: len(prefItems)}
	} else {
		prefMem = search.MemoryBucket{CubeID: memCubeID, Memories: []map[string]any{}, TotalNodes: 0}
	}

	toolMem := search.MemoryBucket{CubeID: memCubeID, Memories: []map[string]any{}, TotalNodes: 0}

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
