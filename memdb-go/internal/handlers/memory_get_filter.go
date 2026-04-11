package handlers

// memory_get_filter.go — native post_get_memory with complex filter support (T5).
// Invoked from NativePostGetMemory when a non-empty filter is present.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/anatolykoptev/memdb/memdb-go/internal/filter"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

const filterGetMemoryMaxLimit = 1000

// handlePostGetMemoryWithFilter executes the native filter path for NativePostGetMemory.
// Called when req.Filter is non-empty and h.postgres != nil.
// cubeID is the primary cube (from mem_cube_id); the caller already validated it is non-empty.
func (h *Handler) handlePostGetMemoryWithFilter(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	req getMemoryRequest,
) {
	memCubeID := *req.MemCubeID
	cubeIDs := []string{memCubeID}

	// Parse and build SQL WHERE conditions from the filter DSL.
	f, err := filter.Parse(req.Filter)
	if err != nil {
		h.writeValidationError(w, []string{"invalid filter: " + err.Error()})
		return
	}
	conditions, err := filter.BuildAGEWhereConditions(f)
	if err != nil {
		h.writeValidationError(w, []string{"invalid filter: " + err.Error()})
		return
	}

	// Limit: default 100, clamp to ≤1000.
	limit := 100
	if req.PageSize != nil && *req.PageSize > 0 {
		limit = *req.PageSize
	}
	if limit > filterGetMemoryMaxLimit {
		limit = filterGetMemoryMaxLimit
	}

	// Cache key includes user + filter hash + limit to prevent cross-user poisoning.
	filterBytes, _ := json.Marshal(req.Filter)
	filterHash := sha256.Sum256(filterBytes)
	cacheKey := cachePrefix + "post_get_memory_filter:" + memCubeID +
		":" + strconv.Itoa(limit) + ":" + hex.EncodeToString(filterHash[:])

	ctx := r.Context()
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cached)
		return
	}

	rows, err := h.postgres.GetMemoriesByFilter(ctx, cubeIDs, conditions, limit)
	if err != nil {
		h.logger.Error("native post_get_memory filter failed",
			slog.String("mem_cube_id", memCubeID),
			slog.Any("error", err),
		)
		// Fall back to proxy on DB error so callers don't see 500s during rollout.
		h.proxyWithBody(w, r, body)
		return
	}

	// Format each raw properties map through the standard MemoryItem formatter.
	memories := make([]map[string]any, 0, len(rows))
	for _, props := range rows {
		item := search.FormatMemoryItem(props, false)
		memories = append(memories, item)
	}

	textBucket := search.MemoryBucket{
		CubeID:     memCubeID,
		Memories:   memories,
		TotalNodes: len(memories),
	}
	emptyBucket := search.MemoryBucket{
		CubeID:     memCubeID,
		Memories:   []map[string]any{},
		TotalNodes: 0,
	}

	// Response shape matches Python handle_get_memories: {text_mem, pref_mem, tool_mem, skill_mem}.
	// Filter path only populates text_mem; pref/tool/skill are empty (no Qdrant query on filter path).
	resp := map[string]any{
		"code":    200,
		"message": "Memories retrieved successfully",
		"data": map[string]any{
			"text_mem":  []search.MemoryBucket{textBucket},
			"pref_mem":  []search.MemoryBucket{emptyBucket},
			"tool_mem":  []search.MemoryBucket{emptyBucket},
			"skill_mem": []search.MemoryBucket{emptyBucket},
		},
	}

	encoded, err := json.Marshal(resp)
	if err != nil {
		h.writeJSON(w, http.StatusOK, resp)
		return
	}

	h.cacheSet(ctx, cacheKey, encoded, getMemoryCacheTTL)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)

	h.logger.Info("native post_get_memory filter complete",
		slog.String("mem_cube_id", memCubeID),
		slog.Int("text_mem", len(memories)),
	)
}
