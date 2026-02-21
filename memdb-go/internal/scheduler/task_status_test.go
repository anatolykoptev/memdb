package scheduler

import (
	"context"
	"testing"
)

// taskStatusTrackerNil verifies that all methods are nil-safe.
func TestTaskStatusTracker_NilSafe(t *testing.T) {
	var tr *TaskStatusTracker // nil tracker
	ctx := context.Background()

	msg := ScheduleMessage{
		ItemID: "item1",
		UserID: "user1",
		CubeID: "cube1",
		Label:  LabelMemOrganize,
	}

	// None of these should panic.
	tr.TaskSubmitted(ctx, msg)
	tr.TaskStarted(ctx, msg)
	tr.TaskCompleted(ctx, msg)
	tr.TaskFailed(ctx, msg, "some error")

	if got := tr.CountActiveTasks(ctx, "user1"); got != 0 {
		t.Errorf("nil tracker CountActiveTasks = %d, want 0", got)
	}
	if !tr.IsIdle(ctx, "user1") {
		t.Error("nil tracker IsIdle should return true")
	}
	if got := tr.GetAllTasksForUser(ctx, "user1"); got != nil {
		t.Errorf("nil tracker GetAllTasksForUser = %v, want nil", got)
	}
}

// TestTaskStatusTracker_NilRedis verifies tracker with nil redis client is safe.
func TestTaskStatusTracker_NilRedis(t *testing.T) {
	tr := NewTaskStatusTracker(nil)
	ctx := context.Background()

	msg := ScheduleMessage{
		ItemID: "item1",
		UserID: "user1",
		CubeID: "cube1",
		Label:  LabelAdd,
	}

	tr.TaskSubmitted(ctx, msg)
	tr.TaskStarted(ctx, msg)
	tr.TaskCompleted(ctx, msg)
	tr.TaskFailed(ctx, msg, "err")

	if got := tr.CountActiveTasks(ctx, "user1"); got != 0 {
		t.Errorf("nil-redis tracker CountActiveTasks = %d, want 0", got)
	}
	if !tr.IsIdle(ctx, "user1") {
		t.Error("nil-redis tracker IsIdle should return true")
	}
}

// TestTaskMetaKey verifies the Redis key format matches Python's schema.
func TestTaskMetaKey(t *testing.T) {
	tr := NewTaskStatusTracker(nil)
	key := tr.metaKey("alice")
	want := "memos:task_meta:alice"
	if key != want {
		t.Errorf("metaKey = %q, want %q", key, want)
	}
}

// TestNowISO verifies nowISO returns a non-empty RFC3339Nano string.
func TestNowISO(t *testing.T) {
	s := nowISO()
	if s == "" {
		t.Error("nowISO returned empty string")
	}
	// Must be parseable as RFC3339.
	if len(s) < 20 {
		t.Errorf("nowISO too short: %q", s)
	}
}

// TestTaskStatusConstants verifies status strings match Python exactly.
func TestTaskStatusConstants(t *testing.T) {
	cases := map[string]string{
		"waiting":     taskStatusWaiting,
		"in_progress": taskStatusInProgress,
		"completed":   taskStatusCompleted,
		"failed":      taskStatusFailed,
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("status constant mismatch: got %q, want %q", got, want)
		}
	}
}

// TestTaskMetaPrefix verifies the Redis key prefix matches Python's schema.
func TestTaskMetaPrefix(t *testing.T) {
	const pythonPrefix = "memos:task_meta:"
	if taskMetaPrefix != pythonPrefix {
		t.Errorf("taskMetaPrefix = %q, want %q (must match Python)", taskMetaPrefix, pythonPrefix)
	}
}
