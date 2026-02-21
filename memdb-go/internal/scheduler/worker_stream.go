package scheduler

// worker_stream.go — Redis Streams I/O: readLoop, reclaimLoop, and helpers.
// Covers: readLoop, scanAndRead, scanStreamKeys, ensureGroup, isBusyGroup,
//         reclaimLoop, reclaimPending.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---- readLoop ---------------------------------------------------------------

// readLoop periodically scans for scheduler stream keys and reads new messages
// from each using XREADGROUP, forwarding them to msgCh.
func (w *Worker) readLoop(ctx context.Context) {
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	// Run immediately on start.
	w.scanAndRead(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.scanAndRead(ctx)
		}
	}
}

// scanAndRead scans Redis for all scheduler stream keys, ensures consumer groups
// exist, and reads a batch of new messages from each stream.
func (w *Worker) scanAndRead(ctx context.Context) {
	keys := w.scanStreamKeys(ctx)
	if len(keys) == 0 {
		return
	}

	for _, key := range keys {
		w.ensureGroup(ctx, key)

		streams, err := w.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    ConsumerGroup,
			Consumer: consumerName,
			Streams:  []string{key, ">"},
			Count:    readBatchSize,
			Block:    blockDuration,
		}).Result()
		if err != nil {
			if !errors.Is(err, redis.Nil) && ctx.Err() == nil {
				w.logger.Debug("scheduler: xreadgroup error",
					slog.String("stream", key), slog.Any("error", err))
			}
			continue
		}

		for _, s := range streams {
			for _, m := range s.Messages {
				sm, err := fromXMessage(s.Stream, m)
				if err != nil {
					w.logger.Warn("scheduler: parse message failed",
						slog.String("stream", s.Stream),
						slog.String("msg_id", m.ID),
						slog.Any("error", err))
					// XACK malformed messages so they don't pile up.
					_ = w.redis.XAck(ctx, s.Stream, ConsumerGroup, m.ID).Err()
					continue
				}
				if err := w.enqueue(ctx, streamMsg{msg: sm}); err != nil {
					return
				}
			}
		}
	}
}

// scanStreamKeys scans Redis for all keys matching the scheduler stream prefix.
// Uses SCAN cursor iteration — production-safe (no KEYS *).
func (w *Worker) scanStreamKeys(ctx context.Context) []string {
	pattern := StreamKeyPrefix + ":*"
	var keys []string
	var cursor uint64
	for {
		batch, next, err := w.redis.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Debug("scheduler: scan error", slog.Any("error", err))
			}
			break
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return keys
}

// ensureGroup creates the consumer group if it does not already exist.
// MKSTREAM creates the stream key if absent (safe for pre-creation).
func (w *Worker) ensureGroup(ctx context.Context, streamKey string) {
	err := w.redis.XGroupCreateMkStream(ctx, streamKey, ConsumerGroup, "0").Err()
	if err != nil && !isBusyGroup(err) {
		w.logger.Debug("scheduler: xgroup create error",
			slog.String("stream", streamKey), slog.Any("error", err))
	}
}

// isBusyGroup returns true if the error is "BUSYGROUP Consumer Group name already exists".
func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "busygroup")
}

// ---- reclaimLoop ------------------------------------------------------------

// reclaimLoop periodically reclaims pending messages that have been idle longer
// than minIdleTime using XAUTOCLAIM. This handles crashes / restarts.
func (w *Worker) reclaimLoop(ctx context.Context) {
	ticker := time.NewTicker(reclaimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.reclaimPending(ctx)
		}
	}
}

// reclaimPending scans all stream keys and XAUTOCLAIMs messages idle > minIdleTime.
func (w *Worker) reclaimPending(ctx context.Context) {
	keys := w.scanStreamKeys(ctx)
	for _, key := range keys {
		startID := "0-0"
		for {
			claimedMsgs, nextStartID, err := w.redis.XAutoClaim(ctx, &redis.XAutoClaimArgs{
				Stream:   key,
				Group:    ConsumerGroup,
				Consumer: consumerName,
				MinIdle:  minIdleTime,
				Start:    startID,
				Count:    readBatchSize,
			}).Result()
			if err != nil {
				if ctx.Err() == nil {
					w.logger.Debug("scheduler: xautoclaim error",
						slog.String("stream", key), slog.Any("error", err))
				}
				break
			}

			for _, m := range claimedMsgs {
				sm, err := fromXMessage(key, m)
				if err != nil {
					_ = w.redis.XAck(ctx, key, ConsumerGroup, m.ID).Err()
					continue
				}
				if err := w.enqueue(ctx, streamMsg{msg: sm}); err != nil {
					return
				}
			}

			// XAutoClaim returns "0-0" as next when there are no more pending entries.
			if nextStartID == "" || nextStartID == "0-0" || len(claimedMsgs) == 0 {
				break
			}
			startID = nextStartID
		}
	}
}
