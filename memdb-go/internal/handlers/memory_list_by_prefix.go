package handlers

// memory_list_by_prefix.go — POST /product/list_memories_by_prefix.
// Anthropic-memory-tool adapter Phase 1: list memories by key prefix
// (the "view directory" operation). Returns lightweight rows (no full
// memory text) so listing pages stay cheap. Backed by trigram GIN index
// on properties.key (migration 0018).

import (
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// listMemoriesByPrefixRequest validates POST /product/list_memories_by_prefix.
//
// Limit defaults to 100, capped at 1000. Offset defaults to 0; negative
// values rejected.
type listMemoriesByPrefixRequest struct {
	CubeID *string `json:"cube_id"`
	UserID *string `json:"user_id"`
	Prefix *string `json:"prefix"`
	Limit  *int    `json:"limit,omitempty"`
	Offset *int    `json:"offset,omitempty"`
}

const (
	listByPrefixDefaultLimit = 100
	listByPrefixMaxLimit     = 1000
)

// NativeListMemoriesByPrefix paginates activated memories whose key starts
// with the given prefix, scoped to (cube_id, user_id).
func (h *Handler) NativeListMemoriesByPrefix(w http.ResponseWriter, r *http.Request) {
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

	var req listMemoriesByPrefixRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	limit, offset, errs := validateListByPrefix(&req)
	if !h.checkErrors(w, errs) {
		return
	}

	ctx := r.Context()
	items, err := h.postgres.ListMemoriesByKeyPrefix(ctx, *req.CubeID, *req.UserID, *req.Prefix, limit, offset)
	if err != nil {
		h.logger.Debug("native list_memories_by_prefix failed",
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
	// Always emit an empty array (not null) for stable client decoding.
	out := items
	if out == nil {
		out = []db.MemoryKeyListItem{}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "ok",
		"data":    out,
	})
}

// validateListByPrefix enforces required fields and numeric bounds.
// Returns the resolved (limit, offset) plus any validation errors.
func validateListByPrefix(req *listMemoriesByPrefixRequest) (int, int, []string) {
	var errs []string
	if req.CubeID == nil || *req.CubeID == "" {
		errs = append(errs, "cube_id is required")
	}
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.Prefix == nil || *req.Prefix == "" {
		errs = append(errs, "prefix is required")
	}
	limit := listByPrefixDefaultLimit
	if req.Limit != nil {
		if *req.Limit < 1 || *req.Limit > listByPrefixMaxLimit {
			errs = append(errs, "limit must be in [1, 1000]")
		} else {
			limit = *req.Limit
		}
	}
	offset := 0
	if req.Offset != nil {
		if *req.Offset < 0 {
			errs = append(errs, "offset must be >= 0")
		} else {
			offset = *req.Offset
		}
	}
	return limit, offset, errs
}
