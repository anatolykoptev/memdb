package handlers

// scheduler_stream.go — native Go replacements for Python's /scheduler/wait and /scheduler/wait/stream.
//
// Python source: src/memos/api/handlers/scheduler_handler.py
//   handle_scheduler_wait()        → NativeSchedulerWait        (POST /product/scheduler/wait)
//   handle_scheduler_wait_stream() → NativeSchedulerWaitStream  (GET  /product/scheduler/wait/stream)
//
// Status source (priority order):
//   1. TaskStatusTracker (memos:task_meta:{user_id} Redis Hash) — exact Python schema,
//      written by Go Worker on every task lifecycle event. Most accurate.
//   2. Fallback: XLen/XPending on scheduler streams — used when tracker not available
//      (e.g. Redis connected but Worker not yet started).
//
// Improvements over Python:
//   - Typed SSE events: "scheduler_update" / "scheduler_done" / "error"
//   - id: <seq> on every event — client can resume with Last-Event-ID after reconnect
//   - retry: 2000ms hint — browser EventSource knows reconnect delay
//   - Heartbeat comment every 15s — keeps connection alive through nginx/caddy
//   - Context-aware: stops immediately on client disconnect
//   - Payload matches Python exactly: user_name, active_tasks, elapsed_seconds, status,
//     timed_out, instance_id

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/MemDBai/MemDB/memdb-go/internal/rpc"
)

// redisClient is the subset of *redis.Client methods used by stream-counting helpers.
type redisClient interface {
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd
	XLen(ctx context.Context, key string) *redis.IntCmd
	XInfoGroups(ctx context.Context, key string) *redis.XInfoGroupsCmd
}

const (
	// sseRetryMs is the reconnect delay sent to the browser EventSource.
	// Browser will reconnect after this many ms on unexpected disconnect.
	sseRetryMs = 2000

	// ssePollInterval is how often we poll Redis for scheduler status.
	ssePollInterval = 500 * time.Millisecond

	// sseHeartbeatInterval is how often we send SSE comment heartbeats.
	// Keeps the connection alive through nginx/caddy proxies.
	sseHeartbeatInterval = 15 * time.Second

	// sseDefaultTimeout is the max stream duration if not specified by client.
	sseDefaultTimeout = 120 * time.Second

	// sseMaxTimeout caps the client-requested timeout.
	sseMaxTimeout = 600 * time.Second

	// sseStatsTimeout is the per-poll context timeout for collecting scheduler stats.
	sseStatsTimeout = 3 * time.Second
)

// schedulerUpdatePayload matches Python's SSE payload exactly.
// Python fields: user_name, active_tasks, elapsed_seconds, status, timed_out, instance_id.
type schedulerUpdatePayload struct {
	UserName       string  `json:"user_name"`        // matches Python field name
	ActiveTasks    int     `json:"active_tasks"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	Status         string  `json:"status"`           // "running" | "idle" | "timeout" | "error"
	TimedOut       bool    `json:"timed_out,omitempty"`
	InstanceID     string  `json:"instance_id,omitempty"`
	// Extended fields (Go-only, ignored by Python clients).
	WaitingTasks   int     `json:"waiting_tasks,omitempty"`
	InProgressTasks int    `json:"in_progress_tasks,omitempty"`
	CompletedTasks int     `json:"completed_tasks,omitempty"`
	FailedTasks    int     `json:"failed_tasks,omitempty"`
}

// NativeSchedulerWait handles POST /product/scheduler/wait
// Blocks until the scheduler is idle for user_id or timeout is reached.
// Mirrors Python's handle_scheduler_wait() — returns JSON (not SSE).
//
// Request body: {"user_name": "<id>", "timeout_seconds": 120}
func (h *Handler) NativeSchedulerWait(w http.ResponseWriter, r *http.Request) {
	if h.redis == nil {
		h.ProxyToProduct(w, r)
		return
	}

	var req struct {
		UserName       string  `json:"user_name"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
		PollInterval   float64 `json:"poll_interval"`
	}
	if err := parseJSONBody(r, &req); err != nil {
		h.writeJSON(w, http.StatusBadRequest,
			map[string]any{"code": 400, "message": "invalid request body", "data": nil})
		return
	}
	if req.UserName == "" {
		h.writeJSON(w, http.StatusBadRequest,
			map[string]any{"code": 400, "message": "user_name is required", "data": nil})
		return
	}

	timeout := sseDefaultTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds * float64(time.Second))
		if timeout > sseMaxTimeout {
			timeout = sseMaxTimeout
		}
	}
	pollInterval := ssePollInterval
	if req.PollInterval > 0 {
		pollInterval = time.Duration(req.PollInterval * float64(time.Second))
	}

	ctx := r.Context()
	start := time.Now()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			elapsed := time.Since(start)
			if elapsed > timeout {
				h.writeJSON(w, http.StatusOK, map[string]any{
					"message": "timeout",
					"data": map[string]any{
						"running_tasks":   h.countActiveTasks(ctx, req.UserName),
						"waited_seconds":  elapsed.Seconds(),
						"timed_out":       true,
						"user_name":       req.UserName,
					},
				})
				return
			}
			if h.isSchedulerIdle(ctx, req.UserName) {
				h.writeJSON(w, http.StatusOK, map[string]any{
					"message": "idle",
					"data": map[string]any{
						"running_tasks":   0,
						"waited_seconds":  elapsed.Seconds(),
						"timed_out":       false,
						"user_name":       req.UserName,
					},
				})
				return
			}
		}
	}
}

// NativeSchedulerWaitStream handles GET /product/scheduler/wait/stream
// Streams real-time scheduler progress as SSE until idle or timeout.
//
// Query params:
//
//	user_id  — filter to a specific user (required for tracker, optional for stream fallback)
//	timeout  — max seconds to stream (default 120, max 600)
//	instance_id — forwarded to payload (matches Python)
func (h *Handler) NativeSchedulerWaitStream(w http.ResponseWriter, r *http.Request) {
	if h.redis == nil {
		h.ProxyToProduct(w, r)
		return
	}

	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = r.URL.Query().Get("user_name") // Python uses user_name
	}
	instanceID := r.URL.Query().Get("instance_id")
	timeout := parseSSETimeout(r.URL.Query().Get("timeout"))

	sw := rpc.NewSSEWriter(w, h.logger)
	if sw == nil {
		h.writeJSON(w, http.StatusInternalServerError,
			map[string]any{"code": 500, "message": "streaming not supported", "data": nil})
		return
	}

	rpc.SSEHeaders(w)
	w.WriteHeader(http.StatusOK)
	_ = sw.Write(rpc.SSEEvent{Retry: sseRetryMs, Data: ""})

	ctx := r.Context()
	start := time.Now()
	var seq int64

	pollTicker := time.NewTicker(ssePollInterval)
	heartbeatTicker := time.NewTicker(sseHeartbeatInterval)
	defer pollTicker.Stop()
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-heartbeatTicker.C:
			sseHeartbeat(w)

		case <-pollTicker.C:
			if done := h.handleSSEPollTick(ctx, sw, userID, instanceID, start, timeout, &seq); done {
				return
			}
		}
	}
}

// sseHeartbeat writes an SSE comment to keep the connection alive.
func sseHeartbeat(w http.ResponseWriter) {
	fmt.Fprintf(w, ": heartbeat\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// handleSSEPollTick handles one poll tick for NativeSchedulerWaitStream.
// Returns true when the stream should be closed (timeout, idle, or write error).
func (h *Handler) handleSSEPollTick(ctx context.Context, sw interface{ Write(rpc.SSEEvent) error }, userID, instanceID string, start time.Time, timeout time.Duration, seq *int64) bool {
	elapsed := time.Since(start)
	*seq++
	eventID := strconv.FormatInt(*seq, 10)

	statsCtx, cancel := context.WithTimeout(ctx, sseStatsTimeout)
	activeTasks, stats := h.collectSchedulerStats(statsCtx, userID)
	cancel()

	if elapsed > timeout {
		payload := buildSchedulerPayload(userID, instanceID, activeTasks, elapsed, "timeout", true, stats)
		data, _ := json.Marshal(payload)
		_ = sw.Write(rpc.SSEEvent{ID: eventID, Event: "scheduler_done", Data: string(data)})
		h.logger.Debug("scheduler stream: timeout",
			slog.String("user_id", userID), slog.Duration("elapsed", elapsed))
		return true
	}

	idle := activeTasks == 0
	status, eventType := "running", "scheduler_update"
	if idle {
		status, eventType = "idle", "scheduler_done"
	}

	payload := buildSchedulerPayload(userID, instanceID, activeTasks, elapsed, status, false, stats)
	data, _ := json.Marshal(payload)
	if err := sw.Write(rpc.SSEEvent{ID: eventID, Event: eventType, Data: string(data)}); err != nil {
		return true
	}

	if idle {
		h.logger.Debug("scheduler stream: idle, closing",
			slog.String("user_id", userID), slog.Duration("elapsed", elapsed))
		return true
	}
	return false
}

// taskStats holds per-status counts from TaskStatusTracker.
type taskStats struct {
	waiting    int
	inProgress int
	completed  int
	failed     int
}

// buildSchedulerPayload constructs the SSE event payload for scheduler status updates.
func buildSchedulerPayload(userID, instanceID string, activeTasks int, elapsed time.Duration, status string, timedOut bool, stats taskStats) schedulerUpdatePayload {
	return schedulerUpdatePayload{
		UserName:        userID,
		ActiveTasks:     activeTasks,
		ElapsedSeconds:  elapsed.Seconds(),
		Status:          status,
		TimedOut:        timedOut,
		InstanceID:      instanceID,
		WaitingTasks:    stats.waiting,
		InProgressTasks: stats.inProgress,
		CompletedTasks:  stats.completed,
		FailedTasks:     stats.failed,
	}
}

// collectSchedulerStats returns active task count and per-status breakdown.
// Uses TaskStatusTracker (memos:task_meta) when available; falls back to XPending.
func (h *Handler) collectSchedulerStats(ctx context.Context, userID string) (activeTasks int, stats taskStats) {
	if h.tracker != nil && userID != "" {
		tasks := h.tracker.GetAllTasksForUser(ctx, userID)
		for _, m := range tasks {
			switch m.Status {
			case "waiting":
				stats.waiting++
				activeTasks++
			case "in_progress":
				stats.inProgress++
				activeTasks++
			case "completed":
				stats.completed++
			case "failed":
				stats.failed++
			}
		}
		return activeTasks, stats
	}
	// Fallback: count pending messages in Redis Streams.
	_, _, pending := h.countSchedulerStreamsForUser(ctx, userID)
	return int(pending), stats
}

// countActiveTasks returns the number of active tasks for a user.
func (h *Handler) countActiveTasks(ctx context.Context, userID string) int {
	active, _ := h.collectSchedulerStats(ctx, userID)
	return active
}

// isSchedulerIdle returns true when the user has no active tasks.
func (h *Handler) isSchedulerIdle(ctx context.Context, userID string) bool {
	return h.countActiveTasks(ctx, userID) == 0
}

// countSchedulerStreamsForUser is like countSchedulerStreams but filters by user_id.
func (h *Handler) countSchedulerStreamsForUser(ctx context.Context, userID string) (streams, totalMessages, totalPending int64) {
	const scanPattern = "scheduler:messages:stream:v2.0:*"
	const batchSize = 100

	rdb := h.redis.Client()
	keys := scanUserStreamKeys(ctx, rdb, scanPattern, userID, batchSize)

	seenBases := make(map[string]struct{})
	for _, k := range keys {
		base := streamBase(k)
		if _, seen := seenBases[base]; seen {
			continue
		}
		seenBases[base] = struct{}{}
		streams++

		msgs, pending := streamMessageCount(ctx, rdb, k)
		totalMessages += msgs
		totalPending += pending
	}
	return streams, totalMessages, totalPending
}

// scanUserStreamKeys scans Redis for stream keys matching the pattern filtered by userID.
func scanUserStreamKeys(ctx context.Context, rdb redisClient, pattern, userID string, batchSize int64) []string {
	var keys []string
	var cursor uint64
	for {
		batch, next, err := rdb.Scan(ctx, cursor, pattern, batchSize).Result()
		if err != nil {
			break
		}
		for _, k := range batch {
			if userID == "" || strings.Contains(k, ":"+userID+":") {
				keys = append(keys, k)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return keys
}

// streamMessageCount returns the total message count and pending count for a stream.
func streamMessageCount(ctx context.Context, rdb redisClient, key string) (messages, pending int64) {
	if n, err := rdb.XLen(ctx, key).Result(); err == nil {
		messages = n
	}
	groups, err := rdb.XInfoGroups(ctx, key).Result()
	if err != nil {
		return
	}
	for _, g := range groups {
		if g.Name == goConsumerGroup {
			pending = g.Pending
			break
		}
	}
	return
}

// parseSSETimeout parses a timeout query param (seconds) with bounds checking.
func parseSSETimeout(s string) time.Duration {
	if s == "" {
		return sseDefaultTimeout
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return sseDefaultTimeout
	}
	d := time.Duration(n * float64(time.Second))
	if d > sseMaxTimeout {
		return sseMaxTimeout
	}
	return d
}
