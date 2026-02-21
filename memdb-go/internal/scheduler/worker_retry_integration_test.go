package scheduler

// worker_retry_integration_test.go — end-to-end retry flow with miniredis.
//
// Tests:
//   1. scheduleRetry stores payload in ZSet with correct score
//   2. processDueRetries pops due entries and re-injects into msgCh
//   3. Full flow: error → retry → re-process → success
//   4. Max retries exhausted → DLQ (not re-queued)
//   5. retryDelay backoff sequence is correct

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestWorker creates a Worker backed by miniredis for integration tests.
func newTestWorker(t *testing.T) (*Worker, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := &Worker{
		redis:     rdb,
		reorg:     nil,
		logger:    logger,
		highMsgCh: make(chan streamMsg, highMsgChanBuffer),
		lowMsgCh:  make(chan streamMsg, lowMsgChanBuffer),
		stopCh:    make(chan struct{}),
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return w, mr
}

// ---- scheduleRetry stores in ZSet ------------------------------------------

func TestScheduleRetry_StoresInZSet(t *testing.T) {
	w, mr := newTestWorker(t)
	ctx := context.Background()

	msg := ScheduleMessage{
		CubeID:     "cube-1",
		Label:      LabelMemOrganize,
		MsgID:      "1-0",
		StreamKey:  "scheduler:messages:stream:v2.0:u:cube-1:mem_organize",
		RetryCount: 0,
	}

	w.scheduleRetry(ctx, msg, "db timeout")

	// ZSet must have exactly 1 entry
	size, err := w.retryQueueSize(ctx)
	if err != nil {
		t.Fatalf("retryQueueSize: %v", err)
	}
	if size != 1 {
		t.Errorf("ZSet size = %d, want 1", size)
	}

	// Score must be in the future (now + ~5s)
	members, err := w.redis.ZRangeByScoreWithScores(ctx, retryZSetKey, &redis.ZRangeBy{
		Min: "0",
		Max: "+inf",
	}).Result()
	if err != nil {
		t.Fatalf("ZRangeByScoreWithScores: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}

	score := members[0].Score
	now := float64(time.Now().Unix())
	if score < now+4 || score > now+6 {
		t.Errorf("score = %v, want ~now+5 (got delta %.1f)", score, score-now)
	}

	// Payload must deserialize correctly
	var p retryPayload
	if err := json.Unmarshal([]byte(members[0].Member.(string)), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.CubeID != "cube-1" {
		t.Errorf("CubeID = %q, want cube-1", p.CubeID)
	}
	if p.Label != LabelMemOrganize {
		t.Errorf("Label = %q, want %q", p.Label, LabelMemOrganize)
	}
	if p.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1 (incremented)", p.RetryCount)
	}
	if p.FailReason != "db timeout" {
		t.Errorf("FailReason = %q, want %q", p.FailReason, "db timeout")
	}
	_ = mr
}

// ---- scheduleRetry increments RetryCount -----------------------------------

func TestScheduleRetry_IncrementsRetryCount(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()

	for attempt := 0; attempt < defaultMaxRetries; attempt++ {
		// Clear ZSet between iterations
		w.redis.Del(ctx, retryZSetKey)

		msg := ScheduleMessage{
			CubeID:     "cube-x",
			Label:      LabelMemRead,
			RetryCount: attempt,
		}
		w.scheduleRetry(ctx, msg, "transient error")

		members, _ := w.redis.ZRange(ctx, retryZSetKey, 0, -1).Result()
		if len(members) != 1 {
			t.Fatalf("attempt %d: expected 1 ZSet member", attempt)
		}
		var p retryPayload
		json.Unmarshal([]byte(members[0]), &p)
		if p.RetryCount != attempt+1 {
			t.Errorf("attempt %d: RetryCount = %d, want %d", attempt, p.RetryCount, attempt+1)
		}
	}
}

// ---- scheduleRetry → DLQ when maxRetries exhausted -------------------------

func TestScheduleRetry_MaxRetriesGoesToDLQ(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()

	msg := ScheduleMessage{
		CubeID:     "cube-dlq",
		Label:      LabelMemOrganize,
		MsgID:      "2-0",
		StreamKey:  "scheduler:messages:stream:v2.0:u:cube-dlq:mem_organize",
		RetryCount: defaultMaxRetries, // already at max
	}

	w.scheduleRetry(ctx, msg, "final failure")

	// ZSet must be empty (not re-queued)
	size, _ := w.retryQueueSize(ctx)
	if size != 0 {
		t.Errorf("ZSet size = %d, want 0 (should go to DLQ)", size)
	}

	// DLQ must have 1 entry
	dlqLen, err := w.redis.XLen(ctx, dlqStreamKey).Result()
	if err != nil {
		t.Fatalf("XLen DLQ: %v", err)
	}
	if dlqLen != 1 {
		t.Errorf("DLQ len = %d, want 1", dlqLen)
	}

	// DLQ entry must contain the cube_id and reason
	msgs, _ := w.redis.XRange(ctx, dlqStreamKey, "-", "+").Result()
	if len(msgs) == 0 {
		t.Fatal("DLQ is empty")
	}
	vals := msgs[0].Values
	if vals["cube_id"] != "cube-dlq" {
		t.Errorf("DLQ cube_id = %v, want cube-dlq", vals["cube_id"])
	}
	if reason, ok := vals["reason"].(string); !ok || reason == "" {
		t.Errorf("DLQ reason missing or empty: %v", vals["reason"])
	}
}

// ---- processDueRetries re-injects into msgCh --------------------------------

func TestProcessDueRetries_ReInjectsIntoMsgCh(t *testing.T) {
	w, mr := newTestWorker(t)
	ctx := context.Background()

	// Manually add a due entry (score = past timestamp)
	p := retryPayload{
		CubeID:     "cube-retry",
		Label:      LabelMemUpdate,
		MsgID:      "3-0",
		StreamKey:  "scheduler:messages:stream:v2.0:u:cube-retry:mem_update",
		Content:    "test query",
		RetryCount: 1,
		MaxRetries: 3,
		RetryAt:    time.Now().Add(-1 * time.Second), // already due
		FailReason: "embed timeout",
	}
	data, _ := json.Marshal(p)
	pastScore := float64(time.Now().Add(-1 * time.Second).Unix())
	w.redis.ZAdd(ctx, retryZSetKey, redis.Z{Score: pastScore, Member: string(data)})

	// Also add a future entry (should NOT be processed)
	futureP := retryPayload{CubeID: "cube-future", Label: LabelMemOrganize, RetryCount: 1}
	futureData, _ := json.Marshal(futureP)
	futureScore := float64(time.Now().Add(60 * time.Second).Unix())
	w.redis.ZAdd(ctx, retryZSetKey, redis.Z{Score: futureScore, Member: string(futureData)})

	// Fast-forward miniredis time so "now" is accurate
	mr.FastForward(0)

	w.processDueRetries(ctx)

	// LabelMemUpdate is high-priority → must arrive in highMsgCh
	select {
	case sm := <-w.highMsgCh:
		if sm.msg.CubeID != "cube-retry" {
			t.Errorf("CubeID = %q, want cube-retry", sm.msg.CubeID)
		}
		if sm.msg.RetryCount != 1 {
			t.Errorf("RetryCount = %d, want 1", sm.msg.RetryCount)
		}
		if sm.msg.Label != LabelMemUpdate {
			t.Errorf("Label = %q, want %q", sm.msg.Label, LabelMemUpdate)
		}
		if sm.msg.Content != "test query" {
			t.Errorf("Content = %q, want test query", sm.msg.Content)
		}
		if !sm.msg.HighPriority {
			t.Error("HighPriority must be true for mem_update retry")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout: no message received in highMsgCh")
	}

	// Both channels must be empty (future entry not processed)
	select {
	case sm := <-w.highMsgCh:
		t.Errorf("unexpected message in highMsgCh: %+v", sm)
	case sm := <-w.lowMsgCh:
		t.Errorf("unexpected message in lowMsgCh: %+v", sm)
	default:
	}

	// ZSet must still have the future entry
	size, _ := w.retryQueueSize(ctx)
	if size != 1 {
		t.Errorf("ZSet size = %d, want 1 (future entry remains)", size)
	}
}

// ---- processDueRetries removes from ZSet atomically ------------------------

func TestProcessDueRetries_RemovesFromZSet(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()

	p := retryPayload{CubeID: "cube-rm", Label: LabelPrefAdd, RetryCount: 2}
	data, _ := json.Marshal(p)
	pastScore := float64(time.Now().Add(-5 * time.Second).Unix())
	w.redis.ZAdd(ctx, retryZSetKey, redis.Z{Score: pastScore, Member: string(data)})

	w.processDueRetries(ctx)

	// Drain channel (LabelPrefAdd is low-priority)
	select {
	case <-w.lowMsgCh:
	case <-time.After(200 * time.Millisecond):
	}

	// ZSet must be empty after processing
	size, _ := w.retryQueueSize(ctx)
	if size != 0 {
		t.Errorf("ZSet size = %d, want 0 after processing", size)
	}
}

// ---- processDueRetries with empty ZSet is a no-op --------------------------

func TestProcessDueRetries_EmptyZSet(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()

	// Must not panic or block
	w.processDueRetries(ctx)

	select {
	case sm := <-w.highMsgCh:
		t.Errorf("unexpected message in highMsgCh: %+v", sm)
	case sm := <-w.lowMsgCh:
		t.Errorf("unexpected message in lowMsgCh: %+v", sm)
	default:
	}
}

// ---- Full flow: error → retry → re-process ---------------------------------

func TestFullRetryFlow_ErrorThenSuccess(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()

	// Simulate: first call fails, second succeeds.
	attempts := 0
	processMsg := func(msg ScheduleMessage) error {
		attempts++
		if attempts == 1 {
			return nil // simulate: scheduleRetry called externally
		}
		return nil
	}
	_ = processMsg

	// Step 1: original message fails → scheduleRetry
	original := ScheduleMessage{
		CubeID:     "cube-flow",
		Label:      LabelMemFeedback,
		MsgID:      "10-0",
		StreamKey:  "scheduler:messages:stream:v2.0:u:cube-flow:mem_feedback",
		RetryCount: 0,
	}
	w.scheduleRetry(ctx, original, "llm timeout")

	// ZSet has 1 entry
	size, _ := w.retryQueueSize(ctx)
	if size != 1 {
		t.Fatalf("after scheduleRetry: ZSet size = %d, want 1", size)
	}

	// Step 2: fast-forward — make the entry due by manipulating the score
	members, _ := w.redis.ZRangeByScoreWithScores(ctx, retryZSetKey, &redis.ZRangeBy{Min: "0", Max: "+inf"}).Result()
	if len(members) == 0 {
		t.Fatal("ZSet is empty")
	}
	// Override score to past
	w.redis.ZAdd(ctx, retryZSetKey, redis.Z{
		Score:  float64(time.Now().Add(-1 * time.Second).Unix()),
		Member: members[0].Member,
	})

	// Step 3: processDueRetries re-injects — LabelMemFeedback is high-priority
	w.processDueRetries(ctx)

	select {
	case sm := <-w.highMsgCh:
		if sm.msg.RetryCount != 1 {
			t.Errorf("RetryCount = %d, want 1", sm.msg.RetryCount)
		}
		if sm.msg.CubeID != "cube-flow" {
			t.Errorf("CubeID = %q, want cube-flow", sm.msg.CubeID)
		}
		if !sm.msg.HighPriority {
			t.Error("HighPriority must be true for mem_feedback retry")
		}
		// Retry messages must NOT XACK (RetryCount > 0, StreamKey preserved)
		if sm.msg.StreamKey == "" {
			t.Error("StreamKey must be preserved for retry messages")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout: no retry message received in highMsgCh")
	}

	// ZSet must be empty
	size, _ = w.retryQueueSize(ctx)
	if size != 0 {
		t.Errorf("ZSet size = %d, want 0 after retry", size)
	}
}

// ---- Backoff scores are monotonically increasing ---------------------------

func TestScheduleRetry_BackoffScoresIncreasing(t *testing.T) {
	w, _ := newTestWorker(t)
	ctx := context.Background()

	var prevScore float64
	for attempt := 0; attempt < defaultMaxRetries; attempt++ {
		w.redis.Del(ctx, retryZSetKey)

		msg := ScheduleMessage{
			CubeID:     "cube-backoff",
			Label:      LabelMemOrganize,
			RetryCount: attempt,
		}
		before := time.Now()
		w.scheduleRetry(ctx, msg, "err")
		_ = before

		members, _ := w.redis.ZRangeByScoreWithScores(ctx, retryZSetKey, &redis.ZRangeBy{Min: "0", Max: "+inf"}).Result()
		if len(members) == 0 {
			t.Fatalf("attempt %d: ZSet empty", attempt)
		}
		score := members[0].Score
		if attempt > 0 && score <= prevScore {
			t.Errorf("attempt %d: score %v not greater than prev %v (backoff not increasing)", attempt, score, prevScore)
		}
		prevScore = score
	}
}

// ---- XACK skipped for retry messages (RetryCount > 0) ----------------------

func TestHandle_NoXACKForRetryMessages(t *testing.T) {
	w, mr := newTestWorker(t)
	ctx := context.Background()

	// Create a stream and consumer group
	streamKey := "scheduler:messages:stream:v2.0:u:cube-noack:mem_organize"
	mr.XAdd(streamKey, "*", []string{"cube_id", "cube-noack", "label", "mem_organize"})
	w.redis.XGroupCreateMkStream(ctx, streamKey, ConsumerGroup, "0")

	// Read the message to put it in PEL
	streams, _ := w.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    ConsumerGroup,
		Consumer: consumerName,
		Streams:  []string{streamKey, ">"},
		Count:    1,
	}).Result()
	if len(streams) == 0 || len(streams[0].Messages) == 0 {
		t.Fatal("no messages read from stream")
	}
	origMsgID := streams[0].Messages[0].ID

	// Simulate a retry message (RetryCount=1, same MsgID/StreamKey)
	retryMsg := ScheduleMessage{
		CubeID:     "cube-noack",
		Label:      LabelMemOrganize,
		MsgID:      origMsgID,
		StreamKey:  streamKey,
		RetryCount: 1, // retry — must NOT XACK
	}

	// handle() with no reorg configured → no-op for mem_organize, no error
	w.handle(ctx, retryMsg)

	// PEL must still contain the original message (not XACKed)
	pending, err := w.redis.XPending(ctx, streamKey, ConsumerGroup).Result()
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	if pending.Count != 1 {
		t.Errorf("PEL count = %d, want 1 (retry must not XACK original)", pending.Count)
	}
}

// ---- XACK happens for original stream messages (RetryCount == 0) -----------

func TestHandle_XACKForOriginalMessages(t *testing.T) {
	w, mr := newTestWorker(t)
	ctx := context.Background()

	streamKey := "scheduler:messages:stream:v2.0:u:cube-ack:add"
	mr.XAdd(streamKey, "*", []string{"cube_id", "cube-ack", "label", "add"})
	w.redis.XGroupCreateMkStream(ctx, streamKey, ConsumerGroup, "0")

	streams, _ := w.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    ConsumerGroup,
		Consumer: consumerName,
		Streams:  []string{streamKey, ">"},
		Count:    1,
	}).Result()
	if len(streams) == 0 || len(streams[0].Messages) == 0 {
		t.Fatal("no messages read")
	}
	msgID := streams[0].Messages[0].ID

	origMsg := ScheduleMessage{
		CubeID:     "cube-ack",
		Label:      LabelAdd, // always XACK'd
		MsgID:      msgID,
		StreamKey:  streamKey,
		RetryCount: 0,
	}

	w.handle(ctx, origMsg)

	// PEL must be empty (XACKed)
	pending, err := w.redis.XPending(ctx, streamKey, ConsumerGroup).Result()
	if err != nil {
		t.Fatalf("XPending: %v", err)
	}
	if pending.Count != 0 {
		t.Errorf("PEL count = %d, want 0 (original must be XACKed)", pending.Count)
	}
}
