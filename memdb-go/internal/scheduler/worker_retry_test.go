package scheduler

import (
	"encoding/json"
	"testing"
	"time"
)

// ---- retryDelay tests -------------------------------------------------------

func TestRetryDelay_FirstAttempt(t *testing.T) {
	msg := ScheduleMessage{RetryCount: 0}
	got := msg.retryDelay()
	if got != retryBaseDelay {
		t.Errorf("retryDelay(0) = %v, want %v", got, retryBaseDelay)
	}
}

func TestRetryDelay_SecondAttempt(t *testing.T) {
	msg := ScheduleMessage{RetryCount: 1}
	got := msg.retryDelay()
	want := retryBaseDelay * 2
	if got != want {
		t.Errorf("retryDelay(1) = %v, want %v", got, want)
	}
}

func TestRetryDelay_ThirdAttempt(t *testing.T) {
	msg := ScheduleMessage{RetryCount: 2}
	got := msg.retryDelay()
	want := retryBaseDelay * 4
	if got != want {
		t.Errorf("retryDelay(2) = %v, want %v", got, want)
	}
}

func TestRetryDelay_Cap(t *testing.T) {
	msg := ScheduleMessage{RetryCount: 100}
	got := msg.retryDelay()
	if got != retryMaxDelay {
		t.Errorf("retryDelay(100) = %v, want cap %v", got, retryMaxDelay)
	}
}

func TestRetryDelay_NeverExceedsCap(t *testing.T) {
	for i := 0; i <= 20; i++ {
		msg := ScheduleMessage{RetryCount: i}
		d := msg.retryDelay()
		if d > retryMaxDelay {
			t.Errorf("retryDelay(%d) = %v exceeds cap %v", i, d, retryMaxDelay)
		}
		if d <= 0 {
			t.Errorf("retryDelay(%d) = %v must be positive", i, d)
		}
	}
}

func TestBackoffSequence(t *testing.T) {
	expected := []time.Duration{
		retryBaseDelay,     // attempt 0 → 5s
		retryBaseDelay * 2, // attempt 1 → 10s
		retryBaseDelay * 4, // attempt 2 → 20s
		retryBaseDelay * 8, // attempt 3 → 40s
	}
	for i, want := range expected {
		msg := ScheduleMessage{RetryCount: i}
		got := msg.retryDelay()
		if got != want && got != retryMaxDelay {
			t.Errorf("attempt %d: delay = %v, want %v", i, got, want)
		}
	}
}

// ---- maxRetries tests -------------------------------------------------------

func TestMaxRetries_Default(t *testing.T) {
	msg := ScheduleMessage{}
	if got := msg.maxRetries(); got != defaultMaxRetries {
		t.Errorf("maxRetries() = %d, want %d", got, defaultMaxRetries)
	}
}

func TestMaxRetries_Custom(t *testing.T) {
	msg := ScheduleMessage{MaxRetries: 5}
	if got := msg.maxRetries(); got != 5 {
		t.Errorf("maxRetries() = %d, want 5", got)
	}
}

func TestMaxRetries_ZeroUsesDefault(t *testing.T) {
	msg := ScheduleMessage{MaxRetries: 0}
	if got := msg.maxRetries(); got != defaultMaxRetries {
		t.Errorf("maxRetries() = %d, want default %d", got, defaultMaxRetries)
	}
}

// ---- retryPayload JSON round-trip -------------------------------------------

func TestRetryPayload_JSONRoundTrip(t *testing.T) {
	ts := time.Date(2026, 2, 19, 12, 0, 0, 0, time.UTC)
	p := retryPayload{
		ItemID:     "item-1",
		UserID:     "user-1",
		CubeID:     "cube-1",
		Label:      LabelMemOrganize,
		Content:    "test content",
		Timestamp:  ts,
		UserName:   "alice",
		TaskID:     "task-1",
		MsgID:      "1234-0",
		StreamKey:  StreamKeyPrefix + ":user-1:cube-1:mem_organize",
		RetryCount: 2,
		MaxRetries: 3,
		RetryAt:    ts.Add(10 * time.Second),
		FailReason: "db timeout",
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got retryPayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.CubeID != p.CubeID {
		t.Errorf("CubeID = %q, want %q", got.CubeID, p.CubeID)
	}
	if got.Label != p.Label {
		t.Errorf("Label = %q, want %q", got.Label, p.Label)
	}
	if got.RetryCount != p.RetryCount {
		t.Errorf("RetryCount = %d, want %d", got.RetryCount, p.RetryCount)
	}
	if got.FailReason != p.FailReason {
		t.Errorf("FailReason = %q, want %q", got.FailReason, p.FailReason)
	}
	if got.MaxRetries != p.MaxRetries {
		t.Errorf("MaxRetries = %d, want %d", got.MaxRetries, p.MaxRetries)
	}
	if !got.RetryAt.Equal(p.RetryAt) {
		t.Errorf("RetryAt = %v, want %v", got.RetryAt, p.RetryAt)
	}
}

func TestRetryPayload_ToScheduleMessage(t *testing.T) {
	ts := time.Now().UTC()
	p := retryPayload{
		CubeID:     "cube-42",
		Label:      LabelMemFeedback,
		Content:    "feedback content",
		Timestamp:  ts,
		RetryCount: 1,
		MaxRetries: 3,
		MsgID:      "999-0",
		StreamKey:  "scheduler:messages:stream:v2.0:u:c:mem_feedback",
	}

	msg := ScheduleMessage{
		ItemID:     p.ItemID,
		UserID:     p.UserID,
		CubeID:     p.CubeID,
		Label:      p.Label,
		Content:    p.Content,
		Timestamp:  p.Timestamp,
		UserName:   p.UserName,
		TaskID:     p.TaskID,
		MsgID:      p.MsgID,
		StreamKey:  p.StreamKey,
		RetryCount: p.RetryCount,
		MaxRetries: p.MaxRetries,
	}

	if msg.CubeID != "cube-42" {
		t.Errorf("CubeID = %q", msg.CubeID)
	}
	if msg.RetryCount != 1 {
		t.Errorf("RetryCount = %d", msg.RetryCount)
	}
	if msg.maxRetries() != 3 {
		t.Errorf("maxRetries() = %d, want 3", msg.maxRetries())
	}
}

// ---- constants sanity -------------------------------------------------------

func TestRetryConstants(t *testing.T) {
	if retryZSetKey == "" {
		t.Error("retryZSetKey must not be empty")
	}
	if defaultMaxRetries <= 0 {
		t.Error("defaultMaxRetries must be positive")
	}
	if retryBaseDelay <= 0 {
		t.Error("retryBaseDelay must be positive")
	}
	if retryMaxDelay < retryBaseDelay {
		t.Error("retryMaxDelay must be >= retryBaseDelay")
	}
	if retryPollInterval <= 0 {
		t.Error("retryPollInterval must be positive")
	}
}

func TestRetryZSetKeyNotConflictsWithDLQ(t *testing.T) {
	if retryZSetKey == dlqStreamKey {
		t.Error("retryZSetKey must differ from dlqStreamKey")
	}
}

func TestRetryZSetKeyPrefix(t *testing.T) {
	if retryZSetKey == StreamKeyPrefix {
		t.Error("retryZSetKey must differ from StreamKeyPrefix")
	}
}

// ---- toAnySlice helper ------------------------------------------------------

func TestToAnySlice_Empty(t *testing.T) {
	got := toAnySlice(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestToAnySlice_Values(t *testing.T) {
	in := []string{"a", "b", "c"}
	got := toAnySlice(in)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, v := range got {
		s, ok := v.(string)
		if !ok || s != in[i] {
			t.Errorf("got[%d] = %v, want %q", i, v, in[i])
		}
	}
}

// ---- newScoreRange helper ----------------------------------------------------

func TestNewScoreRange(t *testing.T) {
	r := newScoreRange(1234567890.0)
	if r.Min != "0" {
		t.Errorf("Min = %q, want \"0\"", r.Min)
	}
	if r.Max == "" {
		t.Error("Max must not be empty")
	}
	if r.Max == "0" {
		t.Error("Max must not be zero for non-zero input")
	}
}

func TestNewScoreRange_Zero(t *testing.T) {
	r := newScoreRange(0)
	if r.Min != "0" {
		t.Errorf("Min = %q, want \"0\"", r.Min)
	}
}

// ---- nowRFC3339 -------------------------------------------------------------

func TestNowRFC3339_Format(t *testing.T) {
	s := nowRFC3339()
	if len(s) == 0 {
		t.Error("nowRFC3339 returned empty string")
	}
	// Must be parseable as RFC3339
	if _, err := time.Parse("2006-01-02T15:04:05Z", s); err != nil {
		t.Errorf("nowRFC3339 = %q, not valid RFC3339: %v", s, err)
	}
}
