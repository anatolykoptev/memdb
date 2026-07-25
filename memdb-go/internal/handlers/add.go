// Package handlers — native memory add pipeline.
//
// File layout (Single Responsibility):
//
//	add.go           — HTTP handler, validation, routing, shared helpers
//	add_fast.go      — fast-mode pipeline (sliding-window → embed → dedup → insert)
//	add_fine.go      — fine-mode pipeline (LLM extraction+dedup → embed → insert)
//	add_windowing.go — sliding-window extraction of messages into text chunks
//	add_props.go     — memory node property construction and source serialization
package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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
	// userConfigCacheTTL is the TTL for user config entries cached in Redis.
	userConfigCacheTTL = 5 * time.Minute
)

// --- Response types (Python-compatible) ---

type addResponseItem struct {
	Memory     string `json:"memory"`
	MemoryID   string `json:"memory_id"`
	MemoryType string `json:"memory_type"`
	CubeID     string `json:"cube_id"`
	Degraded   bool   `json:"degraded,omitempty"`
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
	errs := validateAddRequest(req.UserID, req.AsyncMode, req.Mode)
	errs = append(errs, validateKey(req.Key)...)
	isAsync := req.AsyncMode != nil && *req.AsyncMode == modeAsync
	if !isAsync && len(req.Messages) == 0 {
		errs = append(errs, "messages must not be empty")
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

	// Metrics setup — mode determined before eligibility check so proxy gets correct label.
	mode := addMode(&req)
	modeAttr := metric.WithAttributes(attribute.String("mode", mode))
	start := time.Now()

	// Check native eligibility
	if !h.canHandleNativeAdd(&req) {
		h.logger.Debug("add: cannot handle natively",
			slog.String("reason", h.proxyReason(&req)),
		)
		addMx().Requests.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("mode", mode),
			attribute.String("outcome", "503"),
		))
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "service degraded: postgres unavailable",
			"data":    nil,
		})
		return
	}

	// Ingestion queue: limit concurrent sync adds (async bypasses — already rate-limited by Redis Streams)
	if !isAsync && h.addSem != nil {
		if !h.acquireAddSlot(r.Context()) {
			h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"code": 503, "message": "add queue full, try again later",
			})
			return
		}
		defer h.addSem.Release(1)
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
		// Phase 2: hybrid auto-create. First write to a new cube implicitly
		// creates a cubes row with minimal defaults and logs a warn so that
		// operators can notice clients that should be calling create_cube
		// explicitly with metadata.
		if h.cubeStore != nil {
			created, ensureErr := h.cubeStore.EnsureCubeExists(ctx, cubeID, userID)
			if ensureErr != nil {
				// best-effort: log and continue. The memory write proceeds.
				h.logger.Error("ensure cube failed",
					slog.String("cube_id", cubeID),
					slog.String("owner_id", userID),
					slog.Any("error", ensureErr),
				)
			} else if created {
				h.logger.Warn("cube auto-created without explicit create_cube",
					slog.String("cube_id", cubeID),
					slog.String("owner_id", userID),
				)
			}
		}

		items, err := h.nativeAddForCube(ctx, &req, cubeID)
		if err != nil {
			outcome := addErrorOutcome(err)
			if outcome == "canceled" {
				h.logger.Info("native add canceled",
					slog.String("cube_id", cubeID),
					slog.Any("error", err),
				)
			} else {
				h.logger.Error("native add failed",
					slog.String("cube_id", cubeID),
					slog.Any("error", err),
				)
			}
			recordAddFailure(ctx, outcome)
			addMx().Requests.Add(ctx, 1, metric.WithAttributes(
				attribute.String("mode", mode),
				attribute.String("outcome", outcome),
			))
			addMx().Duration.Record(ctx, float64(time.Since(start).Milliseconds()), modeAttr)
			if outcome == "canceled" {
				h.writeJSON(w, 499, map[string]any{
					"code":    499,
					"message": "client closed request",
					"data":    nil,
				})
			} else {
				h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
					"code":    503,
					"message": "service degraded: postgres unavailable",
					"data":    nil,
				})
			}
			return
		}
		allItems = append(allItems, items...)
	}

	// Invalidate caches for affected users
	for _, cubeID := range cubeIDs {
		h.cacheInvalidate(ctx,
			cachePrefix+"get_all:"+cubeID+":*",
			cachePrefix+"post_get_memory:"+cubeID+":*",
			cachePrefix+"post_get_memory_filter:"+cubeID+":*",
		)
	}

	addMx().Requests.Add(ctx, 1, metric.WithAttributes(
		attribute.String("mode", mode),
		attribute.String("outcome", "success"),
	))
	addMx().Memories.Add(ctx, int64(len(allItems)), modeAttr)
	addMx().Duration.Record(ctx, float64(time.Since(start).Milliseconds()), modeAttr)

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

// addErrorOutcome classifies an add-pipeline error into a bounded outcome
// label for memdb_add_requests_total{outcome} and memdb_add_failures_total
// {reason}. BUG D: context.Canceled is classified distinctly as "canceled"
// (Info-level log, 499 response) instead of being lumped into "error"
// (ERROR-level log, 503 response). context.DeadlineExceeded is "timeout".
// Everything else is "error".
//
// Bounded enum: success, canceled, timeout, error, 503. Callers MUST use
// this function instead of hardcoding "error" so the cancel/timeout
// classification stays consistent.
func addErrorOutcome(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "error"
}

// recordAddFailure bumps memdb_add_failures_total{reason}. BUG B: pre-fix
// there was no dedicated failure counter — the only signal was
// memdb_add_requests_total{outcome="error"}, which conflated cancellations,
// timeouts, and real errors into one bucket. The reason label is the same
// bounded enum as addErrorOutcome plus "llm_exhausted" for the fine→fast
// fallback path.
func recordAddFailure(ctx context.Context, reason string) {
	mx := addMx()
	if mx == nil || mx.AddFailures == nil {
		return
	}
	mx.AddFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// addMode returns a stable mode label for metrics given the add request flags.
// Priority: async > feedback > raw > fast > fine > buffer > default.
func addMode(req *fullAddRequest) string {
	if req.AsyncMode != nil && *req.AsyncMode == modeAsync {
		return "async"
	}
	if req.IsFeedback != nil && *req.IsFeedback {
		return "feedback"
	}
	if req.Mode != nil {
		switch *req.Mode {
		case modeRaw:
			return "raw"
		case modeFast:
			return "fast"
		case modeFine:
			return "fine"
		}
	}
	return "default"
}

// canHandleNativeAdd checks if the request can be handled natively in Go.
func (h *Handler) canHandleNativeAdd(req *fullAddRequest) bool {
	if h.postgres == nil || h.embedder == nil {
		return false
	}
	// Async requires redis for XADD
	if req.AsyncMode != nil && *req.AsyncMode == modeAsync {
		return h.redis != nil
	}
	// Feedback runs its own native pipeline (handleFeedback); requires llmChat.
	if req.IsFeedback != nil && *req.IsFeedback {
		return h.llmChat != nil
	}
	// mode=fast and mode=raw are always native (no LLM needed)
	if req.Mode != nil && (*req.Mode == modeFast || *req.Mode == modeRaw) {
		return true
	}
	// mode=fine (or nil default) requires llmExtractor
	return h.llmExtractor != nil
}

// proxyReason returns a human-readable reason for proxying (for debug logs).
func (h *Handler) proxyReason(req *fullAddRequest) string {
	if h.postgres == nil {
		return "postgres nil"
	}
	if h.embedder == nil {
		return "embedder nil"
	}
	if req.IsFeedback != nil && *req.IsFeedback && h.llmChat == nil {
		return "feedback: llmChat not configured"
	}
	if h.llmExtractor == nil {
		return "llm extractor not configured (fine mode unavailable)"
	}
	if req.AsyncMode != nil && *req.AsyncMode == modeAsync {
		return modeAsync
	}
	return "unknown"
}

// acquireAddSlot tries to acquire a semaphore slot for add processing.
// Returns true if a slot was acquired (caller must Release), false if the queue is full or context cancelled.
func (h *Handler) acquireAddSlot(ctx context.Context) bool {
	if h.addSem.TryAcquire(1) {
		return true
	}
	if h.addWaiters.Add(1) > h.addQueueMax {
		h.addWaiters.Add(-1)
		return false
	}
	err := h.addSem.Acquire(ctx, 1)
	h.addWaiters.Add(-1)
	return err == nil
}

// nativeAddForCube dispatches to async, fast, fine, or buffer pipeline based on mode.
// Default (no mode specified) uses buffer if enabled, otherwise fine/fast.
func (h *Handler) nativeAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	// Feedback has its own pipeline; bypass mode dispatch.
	if req.IsFeedback != nil && *req.IsFeedback {
		return h.handleFeedback(ctx, cubeID, req)
	}
	if req.AsyncMode != nil && *req.AsyncMode == modeAsync {
		return h.nativeAsyncAddForCube(ctx, req, cubeID)
	}
	if req.Mode != nil && *req.Mode == modeRaw {
		return h.nativeRawAddForCube(ctx, req, cubeID)
	}
	if req.Mode != nil && *req.Mode == modeFast {
		return h.nativeFastAddForCube(ctx, req, cubeID)
	}
	if req.Mode != nil && *req.Mode == modeFine {
		return h.nativeFineAddForCube(ctx, req, cubeID)
	}
	// Default mode (no explicit mode): use buffer if enabled and available
	if req.Mode == nil && h.bufferCfg.Enabled && h.redis != nil && h.llmExtractor != nil {
		return h.bufferAddForCube(ctx, req, cubeID)
	}
	// Fallback: sync fine or fast
	if h.llmExtractor != nil {
		return h.nativeFineAddForCube(ctx, req, cubeID)
	}
	return h.nativeFastAddForCube(ctx, req, cubeID)
}

// --- Shared helpers used by add_fast.go and add_fine.go ---

// nowTimestamp returns the current UTC time in the Python-compatible format.
func nowTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000")
}

// observationDateFromMessages returns the latest message's chat_time truncated
// to "YYYY-MM-DD", or "" when no message carries a usable chat_time.
//
// This is the in-conversation date — distinct from server wall-clock at ingest.
// Used by the add pipeline (fast / raw / fine) to stamp memory rows with the
// real conversation timestamp so retrieval clients can prefix candidates with
// the date the fact actually originated, not the date we happened to ingest.
//
// Iterates from the END so the most recent message wins; tolerates messages
// with empty/short chat_time by skipping over them.
func observationDateFromMessages(messages []chatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		ct := messages[i].ChatTime
		if len(ct) >= 10 {
			return ct[:10]
		}
	}
	return ""
}

// conversationNowAnchor returns the temporal "now" anchor for prompt-level
// rules (D6 resolution, F3 event extractor, episodic summary). Prefers the
// latest message's chat_time so LoCoMo / replay / archival ingest resolve
// "yesterday" relative to the conversation date, not server wall-clock today.
// Returns the same Python-compatible RFC3339 microsecond format as
// nowTimestamp() so downstream parsers (parseNowAnchor, formatConversation
// fallback) keep working. Falls back to wall-clock when no chat_time is
// stamped (live-chat callers, buffer flush, admin reprocess).
func conversationNowAnchor(messages []chatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		ct := strings.TrimSpace(messages[i].ChatTime)
		if ct == "" {
			continue
		}
		// Try common formats and re-emit in nowTimestamp() shape.
		layouts := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05.000000",
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		for _, l := range layouts {
			if t, err := time.Parse(l, ct); err == nil {
				return t.UTC().Format("2006-01-02T15:04:05.000000")
			}
		}
	}
	return nowTimestamp()
}

// nowUnix returns the current Unix timestamp in seconds.
func nowUnix() int64 {
	return time.Now().Unix()
}

// stringOrEmpty dereferences a *string, returning "" if nil.
func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefStringOr dereferences a *string, returning def if nil.
func derefStringOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

// derefBoolOr dereferences a *bool, returning def if nil.
func derefBoolOr(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// derefIntOr dereferences a *int, returning def if nil.
func derefIntOr(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

// mapOrEmpty returns m if non-nil, otherwise an empty map.
func mapOrEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// marshalProps marshals a properties map to JSON bytes for DB insertion.
func marshalProps(props map[string]any) ([]byte, error) {
	return json.Marshal(props)
}

// workingBinding returns the background field value linking an LTM node to its WM node.
func workingBinding(wmID string) string {
	return fmt.Sprintf("[working_binding:%s]", wmID)
}

// textHash computes a 16-byte SHA-256 content hash of the normalized text.
// Used for exact-duplicate detection before insert (mirrors redis/agent-memory-server approach).
// Normalization: lowercase + trim whitespace — avoids false negatives from capitalization.
func textHash(text string) string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:16])
}

// cleanupWorkingMemory removes old WorkingMemory nodes beyond the cube's configured limit.
// When VSET cache is active, also evicts deleted nodes from Redis VSET in a pipeline.
// Non-fatal: logs on error.
func (h *Handler) cleanupWorkingMemory(ctx context.Context, cubeID string) {
	limit := h.getWorkingMemoryLimit(ctx, cubeID)
	if h.wmCache != nil {
		// Use RETURNING variant to get deleted IDs for VSET eviction.
		deletedIDs, err := h.postgres.CleanupWorkingMemoryWithIDs(ctx, cubeID, limit)
		if err != nil {
			h.logger.Debug("working memory cleanup failed",
				slog.String("cube_id", cubeID), slog.Any("error", err))
			return
		}
		if len(deletedIDs) > 0 {
			h.logger.Debug("cleaned up old working memory",
				slog.Int("deleted", len(deletedIDs)), slog.String("cube_id", cubeID))
			if err := h.wmCache.VRemBatch(ctx, cubeID, deletedIDs); err != nil {
				h.logger.Debug("vset evict batch failed",
					slog.String("cube_id", cubeID), slog.Any("error", err))
			}
		}
		return
	}
	// Fallback: no VSET, just delete from postgres.
	deleted, err := h.postgres.CleanupWorkingMemory(ctx, cubeID, limit)
	if err != nil {
		h.logger.Debug("working memory cleanup failed",
			slog.String("cube_id", cubeID), slog.Any("error", err))
	} else if deleted > 0 {
		h.logger.Debug("cleaned up old working memory",
			slog.Int64("deleted", deleted), slog.String("cube_id", cubeID))
	}
}

// getWorkingMemoryLimit returns the per-cube WorkingMemory limit from configuration,
// or defaults to maxWorkingMemory if not set or on error.
func (h *Handler) getWorkingMemoryLimit(ctx context.Context, cubeID string) int {
	if h.postgres == nil {
		return maxWorkingMemory
	}

	// Bug 5 fix: cache namespace v2 is keyed by user_id + cube_id so that the
	// PUT /product/users/{user_id}/config invalidator (which only knows user_id)
	// can sweep all of a user's cube configs via wildcard. Resolve owner via
	// cube store; fall back to cubeID as the user segment if unavailable.
	ownerID := cubeID
	if h.cubeStore != nil {
		if cube, err := h.cubeStore.GetCube(ctx, cubeID); err == nil && cube != nil && cube.OwnerID != "" {
			ownerID = cube.OwnerID
		}
	}
	cacheKey := cachePrefix + "config:v2:" + ownerID + ":" + cubeID
	var config map[string]any

	// 1. Try cache
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		if err := json.Unmarshal(cached, &config); err != nil {
			return maxWorkingMemory
		}
	} else {
		// 2. Fetch from DB
		var err error
		config, err = h.postgres.GetUserConfig(ctx, cubeID)
		if err != nil || config == nil {
			config = map[string]any{}
		}
		// Save back to cache
		if encoded, err := json.Marshal(config); err == nil {
			h.cacheSet(ctx, cacheKey, encoded, userConfigCacheTTL)
		}
	}

	// 3. Extract memory_limits.WorkingMemory
	if limits, ok := config["memory_limits"].(map[string]any); ok {
		if wmLimit, ok := limits["WorkingMemory"].(float64); ok {
			return int(wmLimit)
		}
	}

	return maxWorkingMemory
}
