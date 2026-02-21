package scheduler

// worker_retry.go — retry with exponential backoff via Redis Sorted Set.
//
// Design (inspired by MemOS RedisStreamScheduler + hibiken/asynq):
//   - On transient handler error: scheduleRetry() serialises the message to JSON
//     and adds it to a Redis Sorted Set (retryZSetKey) with score = Unix timestamp
//     of the next attempt.
//   - retryLoop polls the ZSet every retryPollInterval, pops due entries
//     (score ≤ now), and re-injects them into msgCh for reprocessing.
//   - After maxRetries exhausted: moveToDLQ.
//
// Redis key: scheduler:retry:v1  (Sorted Set, score = retry_at Unix seconds)

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// retryPayload is the JSON structure stored in the retry ZSet.
type retryPayload struct {
	ItemID     string    `json:"item_id"`
	UserID     string    `json:"user_id"`
	CubeID     string    `json:"cube_id"`
	Label      string    `json:"label"`
	Content    string    `json:"content"`
	Timestamp  time.Time `json:"timestamp"`
	UserName   string    `json:"user_name"`
	TaskID     string    `json:"task_id"`
	MsgID      string    `json:"msg_id"`
	StreamKey  string    `json:"stream_key"`
	RetryCount int       `json:"retry_count"`
	MaxRetries int       `json:"max_retries"`
	RetryAt    time.Time `json:"retry_at"`
	FailReason string    `json:"fail_reason"`
}

// scheduleRetry decides whether to retry or send to DLQ.
// Called from handle() when a handler returns a transient error.
func (w *Worker) scheduleRetry(ctx context.Context, msg ScheduleMessage, reason string) {
	nextRetry := msg.RetryCount + 1
	if nextRetry > msg.maxRetries() {
		w.logger.Warn("scheduler: max retries exhausted, moving to DLQ",
			slog.String("label", msg.Label),
			slog.String("cube_id", msg.CubeID),
			slog.Int("retry_count", msg.RetryCount),
			slog.String("reason", reason),
		)
		w.moveToDLQ(ctx, msg, fmt.Sprintf("max retries (%d) exhausted: %s", msg.maxRetries(), reason))
		return
	}

	delay := msg.retryDelay()
	retryAt := time.Now().Add(delay)

	p := retryPayload{
		ItemID:     msg.ItemID,
		UserID:     msg.UserID,
		CubeID:     msg.CubeID,
		Label:      msg.Label,
		Content:    msg.Content,
		Timestamp:  msg.Timestamp,
		UserName:   msg.UserName,
		TaskID:     msg.TaskID,
		MsgID:      msg.MsgID,
		StreamKey:  msg.StreamKey,
		RetryCount: nextRetry,
		MaxRetries: msg.MaxRetries,
		RetryAt:    retryAt,
		FailReason: reason,
	}

	data, err := json.Marshal(p)
	if err != nil {
		w.logger.Error("scheduler: retry marshal failed, sending to DLQ",
			slog.String("label", msg.Label),
			slog.Any("error", err),
		)
		w.moveToDLQ(ctx, msg, reason)
		return
	}

	score := float64(retryAt.Unix())
	member := string(data)

	if err := w.redis.ZAdd(ctx, retryZSetKey, scoreZ(score, member)).Err(); err != nil {
		w.logger.Error("scheduler: retry zadd failed, sending to DLQ",
			slog.String("label", msg.Label),
			slog.Any("error", err),
		)
		w.moveToDLQ(ctx, msg, reason)
		return
	}

	w.logger.Info("scheduler: task scheduled for retry",
		slog.String("label", msg.Label),
		slog.String("cube_id", msg.CubeID),
		slog.Int("retry_count", nextRetry),
		slog.Int("max_retries", msg.maxRetries()),
		slog.Duration("delay", delay),
		slog.Time("retry_at", retryAt),
		slog.String("reason", reason),
	)
}

// retryLoop polls the retry ZSet for due tasks and re-injects them into highMsgCh or lowMsgCh.
// Runs as a goroutine alongside readLoop and processLoop.
func (w *Worker) retryLoop(ctx context.Context) {
	ticker := time.NewTicker(retryPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.processDueRetries(ctx)
		}
	}
}

// processDueRetries pops all retry entries with score ≤ now and re-injects them.
func (w *Worker) processDueRetries(ctx context.Context) {
	now := float64(time.Now().Unix())

	// ZRANGEBYSCORE + ZREM in a loop (no ZPOPMIN range in older Redis).
	// Use ZRangeByScoreWithScores to get members, then ZREM atomically.
	r := newScoreRange(now)
	members, err := w.redis.ZRangeByScore(ctx, retryZSetKey, &r).Result()
	if err != nil || len(members) == 0 {
		return
	}

	// Remove the popped members atomically before re-injecting.
	// If ZREM fails, the entry will be re-processed on next poll (idempotent).
	removed, err := w.redis.ZRem(ctx, retryZSetKey, toAnySlice(members)...).Result()
	if err != nil {
		w.logger.Debug("scheduler: retry zrem failed", slog.Any("error", err))
		return
	}
	if removed == 0 {
		return
	}

	for _, raw := range members {
		var p retryPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			w.logger.Warn("scheduler: retry unmarshal failed, skipping",
				slog.Any("error", err))
			continue
		}

		msg := ScheduleMessage{
			ItemID:       p.ItemID,
			UserID:       p.UserID,
			CubeID:       p.CubeID,
			Label:        p.Label,
			Content:      p.Content,
			Timestamp:    p.Timestamp,
			UserName:     p.UserName,
			TaskID:       p.TaskID,
			MsgID:        p.MsgID,
			StreamKey:    p.StreamKey,
			RetryCount:   p.RetryCount,
			MaxRetries:   p.MaxRetries,
			HighPriority: isHighPriority(p.Label),
		}

		w.logger.Info("scheduler: retrying task",
			slog.String("label", msg.Label),
			slog.String("cube_id", msg.CubeID),
			slog.Int("retry_count", msg.RetryCount),
			slog.String("priority", priorityLabel(msg.HighPriority)),
			slog.String("fail_reason", p.FailReason),
		)

		if err := w.enqueue(ctx, streamMsg{msg: msg}); err != nil {
			return
		}
	}
}

// retryQueueSize returns the number of tasks currently waiting in the retry ZSet.
// Used in tests and health checks.
func (w *Worker) retryQueueSize(ctx context.Context) (int64, error) {
	return w.redis.ZCard(ctx, retryZSetKey).Result()
}
