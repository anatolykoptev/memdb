package scheduler

import (
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestFromXMessage_UserIDAndCubeIDSeparate verifies that after Phase 2, a
// Redis XAdd payload with distinct user_id and cube_id values round-trips
// through fromXMessage into distinct ScheduleMessage.UserID and CubeID
// fields. This is the scheduler-side contract for Phase 2.
func TestFromXMessage_UserIDAndCubeIDSeparate(t *testing.T) {
	msg := redis.XMessage{
		ID: "1-0",
		Values: map[string]interface{}{
			"item_id":   "item-xyz",
			"user_id":   "krolik",      // person
			"cube_id":   "dozor-facts", // cube
			"label":     "add",
			"content":   `["mem-1","mem-2"]`,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"user_name": "dozor-facts", // partition key = cube_id
			"task_id":   "task-abc",
		},
	}

	streamKey := "scheduler:messages:stream:v2.0:krolik:dozor-facts:add"
	out, err := fromXMessage(streamKey, msg)
	if err != nil {
		t.Fatalf("fromXMessage: %v", err)
	}
	if out.UserID != "krolik" {
		t.Errorf("UserID: got %q want krolik (person)", out.UserID)
	}
	if out.CubeID != "dozor-facts" {
		t.Errorf("CubeID: got %q want dozor-facts", out.CubeID)
	}
	if out.UserName != "dozor-facts" {
		t.Errorf("UserName: got %q want dozor-facts (partition key)", out.UserName)
	}
	if out.ItemID != "item-xyz" {
		t.Errorf("ItemID: got %q", out.ItemID)
	}
	if out.Label != "add" {
		t.Errorf("Label: got %q", out.Label)
	}
}
