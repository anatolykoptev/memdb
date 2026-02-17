// Package handlers — Phase 3: native fast-mode memory add.
// mode="fast" is handled natively in Go; all other modes proxy to Python.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
)

// --- Constants ---

const (
	// windowChars is the approximate character budget per sliding window (~1024 tokens * 4 chars).
	windowChars = 4096
	// overlapChars is the overlap between consecutive windows (~200 tokens * 4 chars).
	overlapChars = 800
	// maxWorkingMemory is the number of newest WorkingMemory nodes to keep per user.
	maxWorkingMemory = 20
	// dedupThreshold is the cosine similarity threshold for deduplication.
	dedupThreshold = 0.92
	// maxMessages is the upper bound on messages per add request.
	maxMessages = 200
	// maxCubeIDs is the upper bound on writable_cube_ids per add request.
	maxCubeIDs = 20
)

// --- Extracted memory from windowing ---

type extractedMemory struct {
	Text       string
	Sources    []map[string]any
	MemoryType string // "LongTermMemory" or "UserMemory"
}

// --- Response types (Python-compatible) ---

type addResponseItem struct {
	Memory     string `json:"memory"`
	MemoryID   string `json:"memory_id"`
	MemoryType string `json:"memory_type"`
	CubeID     string `json:"cube_id"`
}

// --- NativeAdd handler ---

// NativeAdd handles POST /product/add.
// For mode="fast" + sync + no feedback: processes natively in Go.
// All other cases: proxies to Python.
func (h *Handler) NativeAdd(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	// Normalize deprecated fields first (mem_cube_id → writable_cube_ids, etc.)
	body = normalizeAdd(body)

	var req fullAddRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	// Validate required fields
	var errs []string
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.AsyncMode != nil {
		switch *req.AsyncMode {
		case "async", "sync":
		default:
			errs = append(errs, "async_mode must be one of: async, sync")
		}
	}
	if req.Mode != nil {
		switch *req.Mode {
		case "fast", "fine":
		default:
			errs = append(errs, "mode must be one of: fast, fine")
		}
	}
	if len(req.Messages) > maxMessages {
		errs = append(errs, fmt.Sprintf("messages must not exceed %d items", maxMessages))
	}
	if len(req.WritableCubeIDs) > maxCubeIDs {
		errs = append(errs, fmt.Sprintf("writable_cube_ids must not exceed %d items", maxCubeIDs))
	}
	if !h.checkErrors(w, errs) {
		return
	}

	// Check native eligibility
	if !h.canHandleNativeAdd(&req) {
		h.logger.Debug("add: proxying to python",
			slog.String("reason", h.proxyReason(&req)),
		)
		h.proxyWithBody(w, r, body)
		return
	}

	ctx := r.Context()
	userID := *req.UserID

	// Determine cube IDs
	cubeIDs := req.WritableCubeIDs
	if len(cubeIDs) == 0 {
		cubeIDs = []string{userID}
	}

	var allItems []addResponseItem
	for _, cubeID := range cubeIDs {
		items, err := h.nativeAddForCube(ctx, &req, cubeID)
		if err != nil {
			h.logger.Error("native add failed",
				slog.String("cube_id", cubeID),
				slog.Any("error", err),
			)
			// Fall back to proxy on error
			h.proxyWithBody(w, r, body)
			return
		}
		allItems = append(allItems, items...)
	}

	// Invalidate caches for affected users
	for _, cubeID := range cubeIDs {
		h.cacheInvalidate(ctx,
			cachePrefix+"get_all:"+cubeID+":*",
			cachePrefix+"post_get_memory:"+cubeID+":*",
		)
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "Memory added successfully",
		"data":    allItems,
	})

	h.logger.Info("native add complete",
		slog.String("user_id", userID),
		slog.Int("memories_added", len(allItems)),
	)
}

// canHandleNativeAdd checks if the request can be handled natively in Go.
func (h *Handler) canHandleNativeAdd(req *fullAddRequest) bool {
	if h.postgres == nil || h.embedder == nil {
		return false
	}
	// Only handle fast mode (nil defaults to fast)
	if req.Mode != nil && *req.Mode != "fast" {
		return false
	}
	// Don't handle async
	if req.AsyncMode != nil && *req.AsyncMode == "async" {
		return false
	}
	// Don't handle feedback
	if req.IsFeedback != nil && *req.IsFeedback {
		return false
	}
	return true
}

// proxyReason returns a human-readable reason for proxying (for debug logs).
func (h *Handler) proxyReason(req *fullAddRequest) string {
	if h.postgres == nil {
		return "postgres nil"
	}
	if h.embedder == nil {
		return "embedder nil"
	}
	if req.Mode != nil && *req.Mode != "fast" {
		return "mode=" + *req.Mode
	}
	if req.AsyncMode != nil && *req.AsyncMode == "async" {
		return "async"
	}
	if req.IsFeedback != nil && *req.IsFeedback {
		return "feedback"
	}
	return "unknown"
}

// nativeAddForCube processes fast-mode add for a single cube/user.
func (h *Handler) nativeAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	memories := extractFastMemories(req.Messages)
	if len(memories) == 0 {
		return nil, nil
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000")
	sessionID := ""
	if req.SessionID != nil {
		sessionID = *req.SessionID
	}
	info := req.Info
	if info == nil {
		info = map[string]any{}
	}

	var allNodes []db.MemoryInsertNode
	var items []addResponseItem

	for _, mem := range memories {
		// Content hash dedup (if client provided content_hash)
		if contentHash, ok := info["content_hash"].(string); ok && contentHash != "" {
			exists, err := h.postgres.CheckContentHashExists(ctx, contentHash, cubeID)
			if err != nil {
				h.logger.Debug("content hash check failed", slog.Any("error", err))
			}
			if exists {
				h.logger.Debug("skipping duplicate by content_hash",
					slog.String("hash", contentHash),
				)
				continue
			}
		}

		// Generate embedding
		embeddings, err := h.embedder.Embed(ctx, []string{mem.Text})
		if err != nil {
			return nil, fmt.Errorf("embed: %w", err)
		}
		if len(embeddings) == 0 || len(embeddings[0]) == 0 {
			return nil, fmt.Errorf("embedder returned empty result")
		}
		embedding := embeddings[0]
		embeddingStr := db.FormatVector(embedding)

		// Cosine similarity dedup: search for similar existing memories
		searchTypes := []string{"LongTermMemory", "UserMemory", "WorkingMemory"}
		results, err := h.postgres.VectorSearch(ctx, embedding, cubeID, searchTypes, 1)
		if err != nil {
			h.logger.Debug("dedup vector search failed", slog.Any("error", err))
			// Non-fatal: proceed without dedup
		} else if len(results) > 0 && results[0].Score >= dedupThreshold {
			h.logger.Debug("skipping duplicate by cosine similarity",
				slog.Float64("score", results[0].Score),
			)
			continue
		}

		// Build WorkingMemory node
		wmID := uuid.New().String()
		wmProps := buildMemoryProperties(wmID, mem.Text, "WorkingMemory", cubeID, sessionID, now, info, req.CustomTags, mem.Sources, "")
		wmJSON, err := json.Marshal(wmProps)
		if err != nil {
			return nil, fmt.Errorf("marshal wm properties: %w", err)
		}

		// Build LongTermMemory/UserMemory node
		ltID := uuid.New().String()
		background := fmt.Sprintf("[working_binding:%s]", wmID)
		ltProps := buildMemoryProperties(ltID, mem.Text, mem.MemoryType, cubeID, sessionID, now, info, req.CustomTags, mem.Sources, background)
		ltJSON, err := json.Marshal(ltProps)
		if err != nil {
			return nil, fmt.Errorf("marshal lt properties: %w", err)
		}

		allNodes = append(allNodes,
			db.MemoryInsertNode{ID: wmID, PropertiesJSON: wmJSON, EmbeddingVec: embeddingStr},
			db.MemoryInsertNode{ID: ltID, PropertiesJSON: ltJSON, EmbeddingVec: embeddingStr},
		)

		items = append(items, addResponseItem{
			Memory:     mem.Text,
			MemoryID:   ltID,
			MemoryType: mem.MemoryType,
			CubeID:     cubeID,
		})
	}

	if len(allNodes) == 0 {
		return items, nil
	}

	// Batch insert all nodes
	if err := h.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
		return nil, fmt.Errorf("insert nodes: %w", err)
	}

	// Cleanup old WorkingMemory beyond limit
	deleted, err := h.postgres.CleanupWorkingMemory(ctx, cubeID, maxWorkingMemory)
	if err != nil {
		h.logger.Debug("working memory cleanup failed", slog.Any("error", err))
	} else if deleted > 0 {
		h.logger.Debug("cleaned up old working memory",
			slog.Int64("deleted", deleted),
			slog.String("cube_id", cubeID),
		)
	}

	return items, nil
}

// --- Content extraction (sliding window) ---

// extractFastMemories splits messages into sliding windows of ~1024 tokens.
// Each window becomes one memory. User-only windows → UserMemory, mixed → LongTermMemory.
func extractFastMemories(messages []chatMessage) []extractedMemory {
	if len(messages) == 0 {
		return nil
	}

	// Format all messages into a single text block with per-message sources
	type formattedMsg struct {
		text   string
		role   string
		source map[string]any
	}

	var formatted []formattedMsg
	for _, msg := range messages {
		chatTime := msg.ChatTime
		if chatTime == "" {
			chatTime = time.Now().UTC().Format("2006-01-02T15:04:05")
		}
		text := fmt.Sprintf("%s: [%s]: %s", msg.Role, chatTime, msg.Content)
		source := map[string]any{
			"role":      msg.Role,
			"content":   msg.Content,
			"chat_time": chatTime,
		}
		formatted = append(formatted, formattedMsg{text: text, role: msg.Role, source: source})
	}

	// Sliding window over formatted messages
	var results []extractedMemory
	start := 0

	for start < len(formatted) {
		var windowText strings.Builder
		var windowSources []map[string]any
		userOnly := true
		end := start

		for end < len(formatted) {
			line := formatted[end].text + "\n"
			if windowText.Len()+len(line) > windowChars && windowText.Len() > 0 {
				break
			}
			windowText.WriteString(line)
			windowSources = append(windowSources, formatted[end].source)
			if formatted[end].role != "user" {
				userOnly = false
			}
			end++
		}

		if windowText.Len() == 0 {
			break
		}

		memType := "LongTermMemory"
		if userOnly {
			memType = "UserMemory"
		}

		results = append(results, extractedMemory{
			Text:       strings.TrimSpace(windowText.String()),
			Sources:    windowSources,
			MemoryType: memType,
		})

		// Advance with overlap: move start forward so we lose ~(windowChars - overlapChars) chars
		if end >= len(formatted) {
			break
		}

		// Move start forward to create overlap
		overlapTarget := windowText.Len() - overlapChars
		charCount := 0
		newStart := start
		for newStart < end {
			lineLen := len(formatted[newStart].text) + 1 // +1 for \n
			charCount += lineLen
			newStart++
			if charCount >= overlapTarget {
				break
			}
		}
		if newStart == start {
			newStart = start + 1 // ensure forward progress
		}
		start = newStart
	}

	return results
}

// --- Properties construction ---

// buildMemoryProperties constructs the JSONB properties dict matching the Python format.
func buildMemoryProperties(
	id, memory, memoryType, userName, sessionID, timestamp string,
	info map[string]any, customTags []string,
	sources []map[string]any, background string,
) map[string]any {
	tags := []string{"mode:fast"}
	tags = append(tags, customTags...)

	graphID := uuid.New().String()

	props := map[string]any{
		"id":               id,
		"memory":           memory,
		"memory_type":      memoryType,
		"status":           "activated",
		"user_name":        userName,
		"user_id":          userName,
		"session_id":       sessionID,
		"created_at":       timestamp,
		"updated_at":       timestamp,
		"delete_time":      "",
		"delete_record_id": "",
		"tags":             tags,
		"key":              "",
		"usage":            []string{},
		"sources":          serializeSources(sources),
		"background":       background,
		"confidence":       0.99,
		"type":             "fact",
		"info":             info,
		"graph_id":         graphID,
	}
	return props
}

// serializeSources converts each source map to a JSON string, matching Python's format
// where each element in the sources array is a JSON-serialized string.
func serializeSources(sources []map[string]any) []string {
	if len(sources) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(sources))
	for _, src := range sources {
		b, err := json.Marshal(src)
		if err != nil {
			continue
		}
		result = append(result, string(b))
	}
	return result
}

