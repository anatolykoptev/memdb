package handlers

// memory_get.go — native GET handlers: get_memory, get_memory_by_ids, post_get_memory.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

const (
	memoryCacheTTL    = 120 * time.Second // TTL for single-memory cache entries
	getMemoryCacheTTL = 30 * time.Second  // TTL for post_get_memory response cache
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
		_, _ = w.Write(cached)
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
		h.cacheSet(ctx, cacheKey, encoded, memoryCacheTTL)
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
	cachedRaw, missingIDs := h.partitionCacheHits(ctx, ids)

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

	// Cache fresh DB results and index by ID.
	dbByID := h.cacheAndIndexDBResults(ctx, dbResults)

	// Merge cached + fresh results in request order.
	parsed := mergeMemoryResults(ids, cachedRaw, dbByID)

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    parsed,
	})
}

// partitionCacheHits separates IDs into cached (raw JSON bytes) and missing lists.
func (h *Handler) partitionCacheHits(ctx context.Context, ids []string) (map[string][]byte, []string) {
	cached := make(map[string][]byte, len(ids))
	var missing []string
	for _, id := range ids {
		key := cachePrefix + "memory:" + id
		if val := h.cacheGet(ctx, key); val != nil {
			cached[id] = val
		} else {
			missing = append(missing, id)
		}
	}
	return cached, missing
}

// cacheAndIndexDBResults stores fresh DB results in the cache and returns an ID→entry map.
func (h *Handler) cacheAndIndexDBResults(ctx context.Context, dbResults []map[string]any) map[string]map[string]any {
	byID := make(map[string]map[string]any, len(dbResults))
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
		mid, ok := result["memory_id"].(string)
		if !ok {
			continue
		}
		byID[mid] = entry
		resp := map[string]any{"code": 200, "message": "ok", "data": entry}
		if encoded, err := json.Marshal(resp); err == nil {
			h.cacheSet(ctx, cachePrefix+"memory:"+mid, encoded, memoryCacheTTL)
		}
	}
	return byID
}

// mergeMemoryResults combines cached and fresh DB entries in the original request order.
func mergeMemoryResults(ids []string, cachedRaw map[string][]byte, dbByID map[string]map[string]any) []map[string]any {
	parsed := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if raw, ok := cachedRaw[id]; ok {
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
	return parsed
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
	page, pageSize, includePreference, includeSkillMemory := parseGetMemoryPagination(req)

	ctx := r.Context()
	cacheKey := fmt.Sprintf("%spost_get_memory:%s:%d:%d:%t:%t",
		cachePrefix, memCubeID, page, pageSize, includePreference, includeSkillMemory)

	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cached)
		return
	}

	textMem, ok := h.fetchTextMem(ctx, w, r, body, memCubeID, page, pageSize)
	if !ok {
		return
	}

	skillMem := h.fetchSkillMem(ctx, memCubeID, page, pageSize, includeSkillMemory)
	prefMem := h.fetchPrefMem(ctx, memCubeID, pageSize, includePreference)
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
		h.cacheSet(ctx, cacheKey, encoded, getMemoryCacheTTL)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encoded)
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

// parseGetMemoryPagination extracts pagination and filter flags from a getMemoryRequest.
func parseGetMemoryPagination(req getMemoryRequest) (page, pageSize int, includePreference, includeSkillMemory bool) {
	page = 0
	pageSize = 100
	includePreference = true
	includeSkillMemory = true

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
	return
}

// fetchTextMem queries LongTermMemory+UserMemory and writes a proxy response on error.
// Returns (bucket, true) on success; (zero, false) if the handler already responded.
func (h *Handler) fetchTextMem(ctx context.Context, w http.ResponseWriter, r *http.Request, body []byte, cubeID string, page, pageSize int) (search.MemoryBucket, bool) {
	results, total, err := h.postgres.GetAllMemoriesByTypes(ctx, cubeID, []string{"LongTermMemory", "UserMemory"}, page, pageSize)
	if err != nil {
		h.logger.Debug("native post_get_memory: text_mem query failed, proxying", slog.Any("error", err))
		h.proxyWithBody(w, r, body)
		return search.MemoryBucket{}, false
	}
	return formatMemoryBucket(results, cubeID, total), true
}

// fetchSkillMem queries SkillMemory when enabled; returns an empty bucket otherwise.
func (h *Handler) fetchSkillMem(ctx context.Context, cubeID string, page, pageSize int, include bool) search.MemoryBucket {
	empty := search.MemoryBucket{CubeID: cubeID, Memories: []map[string]any{}, TotalNodes: 0}
	if !include {
		return empty
	}
	results, total, err := h.postgres.GetAllMemories(ctx, cubeID, "SkillMemory", page, pageSize)
	if err != nil {
		h.logger.Debug("native post_get_memory: skill_mem query failed", slog.Any("error", err))
		return empty
	}
	return formatMemoryBucket(results, cubeID, total)
}

// fetchPrefMem scrolls Qdrant preference collections when enabled; returns an empty bucket otherwise.
func (h *Handler) fetchPrefMem(ctx context.Context, cubeID string, pageSize int, include bool) search.MemoryBucket {
	empty := search.MemoryBucket{CubeID: cubeID, Memories: []map[string]any{}, TotalNodes: 0}
	if !include || h.qdrant == nil {
		return empty
	}
	items := h.scrollPreferences(ctx, cubeID, pageSize)
	return search.MemoryBucket{CubeID: cubeID, Memories: items, TotalNodes: len(items)}
}
