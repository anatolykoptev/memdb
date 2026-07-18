package scheduler

// task_status.go — TaskStatusTracker: Go port of Python's TaskStatusTracker.
//
// Python source: src/memos/mem_scheduler/utils/status_tracker.py
//
// Redis schema (identical to Python — shared state):
//
//   memos:task_meta:{user_id}          → Redis Hash
//     field: task_id (= ScheduleMessage.ItemID)
//     value: JSON { status, task_type, mem_cube_id, user_id, item_id,
//                   submitted_at, started_at, completed_at, failed_at,
//                   error, updated_at }
//
// Status lifecycle:
//   submitted → task_submitted() → status = "waiting"
//   started   → task_started()   → status = "in_progress"
//   done      → task_completed() → status = "completed"
//   error     → task_failed()    → status = "failed"
//
// TTL: 7 days after completion/failure (matches Python's timedelta(days=7)).
// The key expires automatically — no manual cleanup needed.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// taskMetaPrefix is the Redis Hash key prefix for task metadata.
	// Must match Python's task_meta_prefix = "memos:task_meta:".
	taskMetaPrefix = "memos:task_meta:"

	// taskMetaTTL is how long completed/failed task metadata is kept in Redis.
	// Matches Python's timedelta(days=7).
	taskMetaTTL = 7 * 24 * time.Hour
)

// taskStatus values — must match Python's status strings exactly.
const (
	taskStatusWaiting    = "waiting"
	taskStatusInProgress = "in_progress"
	taskStatusCompleted  = "completed"
	taskStatusFailed     = "failed"
)

// taskMeta is the JSON payload stored per task in the Redis Hash.
type taskMeta struct {
	Status      string `json:"status"`
	TaskType    string `json:"task_type,omitempty"` // = label (mem_update, mem_organize, …)
	MemCubeID   string `json:"mem_cube_id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	ItemID      string `json:"item_id,omitempty"`
	SubmittedAt string `json:"submitted_at,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	FailedAt    string `json:"failed_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

// TaskStatusTracker writes task lifecycle events to Redis in the same schema
// as Python's TaskStatusTracker. This allows the Python API's
// /product/scheduler/wait and /product/scheduler/wait/stream endpoints to
// observe tasks submitted and processed by the Go worker.
type TaskStatusTracker struct {
	rdb *redis.Client
}

// NewTaskStatusTracker creates a tracker backed by the given Redis client.
func NewTaskStatusTracker(rdb *redis.Client) *TaskStatusTracker {
	return &TaskStatusTracker{rdb: rdb}
}

// metaKey returns the Redis Hash key for a user's task metadata.
func (t *TaskStatusTracker) metaKey(userID string) string {
	return taskMetaPrefix + userID
}

// TaskSubmitted records a new task with status "waiting".
// Called before the message enters the processing channel.
func (t *TaskStatusTracker) TaskSubmitted(ctx context.Context, msg ScheduleMessage) {
	if t == nil || t.rdb == nil {
		return
	}
	now := nowISO()
	meta := taskMeta{
		Status:      taskStatusWaiting,
		TaskType:    msg.Label,
		MemCubeID:   msg.CubeID,
		UserID:      msg.UserID,
		ItemID:      msg.ItemID,
		SubmittedAt: now,
		UpdatedAt:   now,
	}
	t.hset(ctx, msg.UserID, msg.ItemID, meta)
}

// TaskStarted transitions a task to "in_progress".
// Called at the start of handle().
func (t *TaskStatusTracker) TaskStarted(ctx context.Context, msg ScheduleMessage) {
	if t == nil || t.rdb == nil {
		return
	}
	meta := t.load(ctx, msg.UserID, msg.ItemID)
	meta.Status = taskStatusInProgress
	meta.StartedAt = nowISO()
	meta.UpdatedAt = nowISO()
	t.hset(ctx, msg.UserID, msg.ItemID, meta)
}

// TaskCompleted transitions a task to "completed" and sets a 7-day TTL.
// Called after successful handle().
func (t *TaskStatusTracker) TaskCompleted(ctx context.Context, msg ScheduleMessage) {
	if t == nil || t.rdb == nil {
		return
	}
	meta := t.load(ctx, msg.UserID, msg.ItemID)
	meta.Status = taskStatusCompleted
	meta.CompletedAt = nowISO()
	meta.UpdatedAt = nowISO()
	t.hset(ctx, msg.UserID, msg.ItemID, meta)
	t.rdb.Expire(ctx, t.metaKey(msg.UserID), taskMetaTTL)
}

// TaskFailed transitions a task to "failed" with an error message.
// Called when handle() returns a non-retryable error or max retries exceeded.
func (t *TaskStatusTracker) TaskFailed(ctx context.Context, msg ScheduleMessage, errMsg string) {
	if t == nil || t.rdb == nil {
		return
	}
	meta := t.load(ctx, msg.UserID, msg.ItemID)
	meta.Status = taskStatusFailed
	meta.Error = errMsg
	meta.FailedAt = nowISO()
	meta.UpdatedAt = nowISO()
	t.hset(ctx, msg.UserID, msg.ItemID, meta)
	t.rdb.Expire(ctx, t.metaKey(msg.UserID), taskMetaTTL)
}

// GetAllTasksForUser returns all task metadata for a user.
// Used by NativeSchedulerStatus and NativeSchedulerWaitStream.
func (t *TaskStatusTracker) GetAllTasksForUser(ctx context.Context, userID string) map[string]taskMeta {
	if t == nil || t.rdb == nil {
		return nil
	}
	raw, err := t.rdb.HGetAll(ctx, t.metaKey(userID)).Result()
	if err != nil || len(raw) == 0 {
		return nil
	}
	result := make(map[string]taskMeta, len(raw))
	for taskID, jsonStr := range raw {
		var m taskMeta
		if json.Unmarshal([]byte(jsonStr), &m) == nil {
			result[taskID] = m
		}
	}
	return result
}

// CountActiveTasks returns the number of tasks in "waiting" or "in_progress" state.
func (t *TaskStatusTracker) CountActiveTasks(ctx context.Context, userID string) int {
	tasks := t.GetAllTasksForUser(ctx, userID)
	n := 0
	for _, m := range tasks {
		if m.Status == taskStatusWaiting || m.Status == taskStatusInProgress {
			n++
		}
	}
	return n
}

// IsIdle returns true if the user has no active (waiting/in_progress) tasks.
func (t *TaskStatusTracker) IsIdle(ctx context.Context, userID string) bool {
	return t.CountActiveTasks(ctx, userID) == 0
}

// --- internal helpers -------------------------------------------------------

func (t *TaskStatusTracker) load(ctx context.Context, userID, taskID string) taskMeta {
	raw, err := t.rdb.HGet(ctx, t.metaKey(userID), taskID).Result()
	if err != nil {
		return taskMeta{}
	}
	var m taskMeta
	json.Unmarshal([]byte(raw), &m) //nolint:errcheck
	return m
}

func (t *TaskStatusTracker) hset(ctx context.Context, userID, taskID string, meta taskMeta) {
	b, err := json.Marshal(meta)
	if err != nil {
		return
	}
	t.rdb.HSet(ctx, t.metaKey(userID), taskID, string(b))
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// --- watchdog (PF-9 / #327) -------------------------------------------------
//
// Tasks in "in_progress" that never transition to "completed" or "failed"
// (worker crash, panic, OOM kill) stay stuck forever. CountActiveTasks counts
// them as active, IsIdle returns false, and /product/scheduler/wait hangs.
//
// ReclaimStaleTasks scans a user's tasks and transitions any "in_progress"
// task older than maxAge to "failed" with a watchdog error message. Returns
// the number of tasks reclaimed.

// defaultStaleTaskTimeout is the maximum age of an "in_progress" task before
// the watchdog reclaims it. Env-tunable via MEMDB_TASK_STALE_TIMEOUT_M.
const defaultStaleTaskTimeout = 30 * time.Minute

// ReclaimStaleTasks transitions "in_progress" tasks older than maxAge to
// "failed". Returns the count of reclaimed tasks. Best-effort: errors are
// logged and skipped, not returned.
func (t *TaskStatusTracker) ReclaimStaleTasks(ctx context.Context, userID string, maxAge time.Duration) int {
	if t == nil || t.rdb == nil {
		return 0
	}
	tasks := t.GetAllTasksForUser(ctx, userID)
	if len(tasks) == 0 {
		return 0
	}
	now := time.Now().UTC()
	reclaimed := 0
	for taskID, m := range tasks {
		if m.Status != taskStatusInProgress {
			continue
		}
		startedAt, err := time.Parse(time.RFC3339Nano, m.StartedAt)
		if err != nil {
			// Unparseable timestamp → can't determine age, skip.
			continue
		}
		if now.Sub(startedAt) < maxAge {
			continue
		}
		// Stale: transition to failed.
		m.Status = taskStatusFailed
		m.Error = "watchdog: task timed out (stuck in in_progress)"
		m.FailedAt = nowISO()
		m.UpdatedAt = nowISO()
		t.hset(ctx, userID, taskID, m)
		t.rdb.Expire(ctx, t.metaKey(userID), taskMetaTTL)
		reclaimed++
	}
	return reclaimed
}

// RunWatchdog periodically scans all users for stale tasks and reclaims them.
// Blocks until ctx is cancelled. Call as a goroutine:
//
//	go tracker.RunWatchdog(ctx, 5*time.Minute, defaultStaleTaskTimeout)
//
// The scan interval should be shorter than the stale timeout to catch stuck
// tasks promptly. Uses SCAN to enumerate user keys (no KEYS blocking).
func (t *TaskStatusTracker) RunWatchdog(ctx context.Context, scanInterval, maxAge time.Duration) {
	if t == nil || t.rdb == nil {
		return
	}
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.scanAndReclaim(ctx, maxAge)
		}
	}
}

// scanAndReclaim enumerates all task_meta keys via SCAN and reclaims stale
// tasks per user. Best-effort: errors are silently skipped.
func (t *TaskStatusTracker) scanAndReclaim(ctx context.Context, maxAge time.Duration) {
	var cursor uint64
	for {
		keys, next, err := t.rdb.Scan(ctx, cursor, taskMetaPrefix+"*", 100).Result()
		if err != nil {
			return
		}
		for _, key := range keys {
			// Extract userID from key: "memos:task_meta:{user_id}"
			userID := key[len(taskMetaPrefix):]
			if userID == "" {
				continue
			}
			t.ReclaimStaleTasks(ctx, userID, maxAge)
		}
		if next == 0 {
			break
		}
		cursor = next
	}
}
