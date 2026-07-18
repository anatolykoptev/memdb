package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestReclaimStaleTasks_ReclaimsStuckInProgress asserts that an in_progress
// task older than maxAge is transitioned to "failed" by the watchdog.
//
// RED: before the fix, no ReclaimStaleTasks method existed, so stuck tasks
// stayed in_progress forever and IsIdle never returned true.
func TestReclaimStaleTasks_ReclaimsStuckInProgress(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	tr := NewTaskStatusTracker(rdb)
	ctx := context.Background()

	msg := ScheduleMessage{
		ItemID: "task-stuck",
		UserID: "user1",
		CubeID: "cube1",
		Label:  LabelAdd,
	}

	// Submit + start the task.
	tr.TaskSubmitted(ctx, msg)
	tr.TaskStarted(ctx, msg)

	// Manually set StartedAt to 31 minutes ago (ReclaimStaleTasks uses
	// time.Now(), not Redis server time, so mr.FastForward won't help).
	oldStarted := time.Now().UTC().Add(-31 * time.Minute).Format(time.RFC3339Nano)
	tasks := tr.GetAllTasksForUser(ctx, "user1")
	m := tasks["task-stuck"]
	m.StartedAt = oldStarted
	tr.hset(ctx, "user1", "task-stuck", m)

	// Reclaim with maxAge=30min → should reclaim the task.
	reclaimed := tr.ReclaimStaleTasks(ctx, "user1", 30*time.Minute)
	if reclaimed != 1 {
		t.Fatalf("expected 1 reclaimed, got %d", reclaimed)
	}

	// Verify the task is now "failed".
	postTasks := tr.GetAllTasksForUser(ctx, "user1")
	m, ok := postTasks["task-stuck"]
	if !ok {
		t.Fatal("task not found after reclaim")
	}
	if m.Status != taskStatusFailed {
		t.Errorf("status = %q, want %q", m.Status, taskStatusFailed)
	}
	if m.Error == "" {
		t.Error("error message should be set by watchdog")
	}

	// IsIdle should now return true (no active tasks).
	if !tr.IsIdle(ctx, "user1") {
		t.Error("IsIdle should be true after reclaiming all stuck tasks")
	}
}

// TestReclaimStaleTasks_SkipsRecentTasks asserts that recent in_progress
// tasks are NOT reclaimed (only stale ones).
func TestReclaimStaleTasks_SkipsRecentTasks(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	tr := NewTaskStatusTracker(rdb)
	ctx := context.Background()

	msg := ScheduleMessage{
		ItemID: "task-fresh",
		UserID: "user2",
		CubeID: "cube2",
		Label:  LabelAdd,
	}
	tr.TaskSubmitted(ctx, msg)
	tr.TaskStarted(ctx, msg)

	// Set StartedAt to 5 minutes ago — task is NOT stale (maxAge=30min).
	recentStarted := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339Nano)
	tasks := tr.GetAllTasksForUser(ctx, "user2")
	m := tasks["task-fresh"]
	m.StartedAt = recentStarted
	tr.hset(ctx, "user2", "task-fresh", m)

	reclaimed := tr.ReclaimStaleTasks(ctx, "user2", 30*time.Minute)
	if reclaimed != 0 {
		t.Fatalf("expected 0 reclaimed (task is fresh), got %d", reclaimed)
	}

	// Task should still be in_progress.
	postTasks := tr.GetAllTasksForUser(ctx, "user2")
	if postTasks["task-fresh"].Status != taskStatusInProgress {
		t.Errorf("fresh task should still be in_progress, got %q", postTasks["task-fresh"].Status)
	}
}

// TestReclaimStaleTasks_SkipsCompletedTasks asserts that completed/failed
// tasks are not affected by the watchdog.
func TestReclaimStaleTasks_SkipsCompletedTasks(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	tr := NewTaskStatusTracker(rdb)
	ctx := context.Background()

	msg := ScheduleMessage{
		ItemID: "task-done",
		UserID: "user3",
		CubeID: "cube3",
		Label:  LabelAdd,
	}
	tr.TaskSubmitted(ctx, msg)
	tr.TaskStarted(ctx, msg)
	tr.TaskCompleted(ctx, msg)

	// Completed tasks are skipped regardless of age.
	reclaimed := tr.ReclaimStaleTasks(ctx, "user3", 30*time.Minute)
	if reclaimed != 0 {
		t.Fatalf("expected 0 reclaimed (task is completed), got %d", reclaimed)
	}

	tasks := tr.GetAllTasksForUser(ctx, "user3")
	if tasks["task-done"].Status != taskStatusCompleted {
		t.Errorf("completed task should stay completed, got %q", tasks["task-done"].Status)
	}
}

// TestReclaimStaleTasks_NilSafe verifies nil-safety.
func TestReclaimStaleTasks_NilSafe(t *testing.T) {
	var tr *TaskStatusTracker
	if got := tr.ReclaimStaleTasks(context.Background(), "user", 30*time.Minute); got != 0 {
		t.Errorf("nil tracker: expected 0, got %d", got)
	}
	tr = NewTaskStatusTracker(nil)
	if got := tr.ReclaimStaleTasks(context.Background(), "user", 30*time.Minute); got != 0 {
		t.Errorf("nil-redis tracker: expected 0, got %d", got)
	}
}

// TestScanAndReclaim_ReclaimsStaleTasksAcrossUsers verifies the watchdog's
// scanAndReclaim finds all users with stuck tasks via SCAN and reclaims them.
func TestScanAndReclaim_ReclaimsStaleTasksAcrossUsers(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	tr := NewTaskStatusTracker(rdb)
	ctx := context.Background()

	// Create stuck tasks for 2 users.
	for _, userID := range []string{"userA", "userB"} {
		msg := ScheduleMessage{
			ItemID: "task-" + userID,
			UserID: userID,
			CubeID: "cube-" + userID,
			Label:  LabelAdd,
		}
		tr.TaskSubmitted(ctx, msg)
		tr.TaskStarted(ctx, msg)
		// Set StartedAt to 31 minutes ago.
		oldStarted := time.Now().UTC().Add(-31 * time.Minute).Format(time.RFC3339Nano)
		tasks := tr.GetAllTasksForUser(ctx, userID)
		m := tasks["task-"+userID]
		m.StartedAt = oldStarted
		tr.hset(ctx, userID, "task-"+userID, m)
	}

	// Run scanAndReclaim directly (not via goroutine) to avoid timing flakiness.
	tr.scanAndReclaim(ctx, 30*time.Minute)

	// Both users should have their stuck task reclaimed.
	for _, userID := range []string{"userA", "userB"} {
		postTasks := tr.GetAllTasksForUser(ctx, userID)
		taskID := "task-" + userID
		m, ok := postTasks[taskID]
		if !ok {
			t.Errorf("%s: task not found after scanAndReclaim", userID)
			continue
		}
		if m.Status != taskStatusFailed {
			t.Errorf("%s: status = %q, want %q", userID, m.Status, taskStatusFailed)
		}
	}
}
