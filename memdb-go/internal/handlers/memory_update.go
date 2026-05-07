package handlers

// memory_update.go — native POST /product/update_memory handler.
// Replaces text + info + embedding for a single memory node atomically,
// avoiding the delete-then-add race window used by older clients
// (go-wowa experience memory).

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// memoryUpdater is a narrow interface for updating a memory node and
// clearing its cross-encoder score cache after a content change.
// Implemented by *db.Postgres in production and by ceCacheRecorder / stubMemoryUpdater in tests.
type memoryUpdater interface {
	UpdateMemoryByID(ctx context.Context, memoryID, cubeID string, propsJSON []byte, embedding string) error
	// ClearCEScoresTopK removes the ce_score_topk cache for the memory whose
	// text just changed (cached scores computed against old text are stale).
	ClearCEScoresTopK(ctx context.Context, memoryID string) error
	// ClearCEScoresTopKForNeighbor cascades invalidation: any memory that had
	// the updated memory as a neighbor in its own top-K needs to be cleared too.
	ClearCEScoresTopKForNeighbor(ctx context.Context, neighborID string) error
}

// memUpdater returns the memoryUpdater to use: test override if set, otherwise h.postgres.
func (h *Handler) memUpdater() memoryUpdater {
	if h.memUpdaterField != nil {
		return h.memUpdaterField
	}
	return h.postgres
}

// updateMemoryRequest is the POST /product/update_memory payload.
type updateMemoryRequest struct {
	MemoryID *string        `json:"memory_id"`
	UserID   *string        `json:"user_id"` // cube id
	Text     *string        `json:"text"`
	Info     map[string]any `json:"info,omitempty"`
}

// NativeUpdateMemory replaces a memory node's text+embedding+info atomically.
//
// Request body:
//
//	{"memory_id": "<uuid>", "user_id": "<cube>", "text": "...", "info": {...}}
//
// Implementation: re-embeds the new text via the same ONNX path as add_raw,
// rebuilds the properties map using buildNodeProps (raw-mode shape), and
// runs UPDATE in a single statement scoped by (memory_id, cube_id).
//
// Side-effect: created_at is reset to the update time — the original
// creation timestamp is not preserved. Acceptable for experience memory
// updates where temporal ordering by updated_at is what matters.
func (h *Handler) NativeUpdateMemory(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req updateMemoryRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	var errs []string
	if req.MemoryID == nil || *req.MemoryID == "" {
		errs = append(errs, "memory_id is required")
	}
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.Text == nil || *req.Text == "" {
		errs = append(errs, "text is required")
	}
	if !h.checkErrors(w, errs) {
		return
	}

	if h.postgres == nil || h.embedder == nil {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "update unavailable",
			"data":    nil,
		})
		return
	}

	ctx := r.Context()
	cubeID := *req.UserID
	text := *req.Text
	memoryID := *req.MemoryID

	embedding, err := h.embedSingle(ctx, text)
	if err != nil {
		h.logger.Error("update: embed failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    500,
			"message": "embed failed: " + err.Error(),
			"data":    nil,
		})
		return
	}

	info := mapOrEmpty(req.Info)
	info["content_hash"] = textHash(text)
	props := buildNodeProps(memoryNodeProps{
		ID:         memoryID,
		Memory:     text,
		MemoryType: memTypeLongTerm,
		UserName:   cubeID,
		AgentID:    "",
		SessionID:  "",
		Mode:       modeRaw,
		Now:        nowTimestamp(),
		CreatedAt:  nowTimestamp(),
		Info:       info,
		CustomTags: nil,
		Sources:    nil,
		Background: "",
		// Update path re-builds the full properties map — stamp as extracted
		// since the content is available at update time (no pending enrichment).
		ExtractionState: extractionStateExtracted,
	})

	propsJSON, err := marshalProps(props)
	if err != nil {
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    500,
			"message": "marshal props: " + err.Error(),
			"data":    nil,
		})
		return
	}

	updater := h.memUpdater()
	if err := updater.UpdateMemoryByID(ctx, memoryID, cubeID, propsJSON, db.FormatVector(embedding)); err != nil {
		if errors.Is(err, db.ErrMemoryNotFound) {
			h.logger.Info("update_memory: target not found (likely consolidated)",
				slog.String("memory_id", memoryID),
				slog.String("cube_id", cubeID))
			h.writeJSON(w, http.StatusNotFound, map[string]any{
				"code":    404,
				"message": err.Error(),
				"data":    nil,
			})
			return
		}
		h.logger.Error("update memory failed",
			slog.Any("error", err),
			slog.String("memory_id", memoryID),
			slog.String("cube_id", cubeID))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    500,
			"message": "update failed: " + err.Error(),
			"data":    nil,
		})
		return
	}

	// Invalidate cross-encoder score cache: the memory's text just changed, so
	// any pre-computed CE scores referencing it are stale.
	// Order: clear direct cache first, then cascade to neighbors.
	// Best-effort — a cache miss is preferable to stale scores; log errors but
	// do NOT fail the request.
	if err := updater.ClearCEScoresTopK(ctx, memoryID); err != nil {
		h.logger.Error("update_memory: ClearCEScoresTopK failed (best-effort, ignoring)",
			slog.Any("error", err),
			slog.String("memory_id", memoryID))
	}
	if err := updater.ClearCEScoresTopKForNeighbor(ctx, memoryID); err != nil {
		h.logger.Error("update_memory: ClearCEScoresTopKForNeighbor failed (best-effort, ignoring)",
			slog.Any("error", err),
			slog.String("memory_id", memoryID))
	}

	// Invalidate all caches that may hold the pre-update version:
	// paginated listings, vector search results, and the per-id direct get.
	// Matches the pattern used by invalidateDeleteCaches in memory_delete.go.
	h.cacheInvalidate(ctx,
		cachePrefix+"get_all:"+cubeID+":*",
		cachePrefix+"search:*:"+cubeID+":*",
		cachePrefix+"memory:"+memoryID,
		cachePrefix+"post_get_memory:"+cubeID+":*",
		cachePrefix+"post_get_memory_filter:"+cubeID+":*",
	)

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "memory updated",
		"data":    map[string]any{"memory_id": memoryID, "cube_id": cubeID},
	})
}
