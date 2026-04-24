package scheduler

// worker_periodic.go — periodic reorganization loop and Dead Letter Queue.
// Covers: periodicReorgLoop, runPeriodicReorg, getActiveCubes, moveToDLQ.

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// dlqStreamKey is the Redis Stream key for unprocessable messages.
// Capped at 1000 entries (MAXLEN ~) to prevent unbounded growth.
const dlqStreamKey = "scheduler:dlq:v1"

// ---- Dead Letter Queue ------------------------------------------------------

// moveToDLQ writes a failed message to the Dead Letter Queue stream.
// Inspired by hibiken/asynq archived queue pattern.
// The DLQ is a regular Redis Stream — inspect with XRANGE scheduler:dlq:v1 - +
func (w *Worker) moveToDLQ(ctx context.Context, msg ScheduleMessage, reason string) {
	w.logger.Warn("scheduler: moving message to DLQ",
		slog.String("stream", msg.StreamKey),
		slog.String("msg_id", msg.MsgID),
		slog.String("cube_id", msg.CubeID),
		slog.String("label", msg.Label),
		slog.String("reason", reason),
	)
	if err := w.redis.XAdd(ctx, &redis.XAddArgs{
		Stream: dlqStreamKey,
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{
			"origin_stream": msg.StreamKey,
			"origin_msg_id": msg.MsgID,
			"cube_id":       msg.CubeID,
			"label":         msg.Label,
			"reason":        reason,
			"failed_at":     time.Now().UTC().Format(time.RFC3339),
		},
	}).Err(); err != nil {
		w.logger.Debug("scheduler: dlq write failed", slog.Any("error", err))
	}
}

// ---- periodicReorgLoop ------------------------------------------------------

// periodicReorgLoop runs the Memory Reorganizer for every active cube on a
// fixed timer, independent of incoming stream messages.
//
// "Active cubes" are discovered by scanning VSET keys (wm:v:*) — any cube
// that has WorkingMemory in the hot cache is considered active.
// This mirrors MemOS RedisStreamsScheduler periodic timer pattern.
func (w *Worker) periodicReorgLoop(ctx context.Context) {
	// Stagger first run by half the interval so it doesn't overlap with startup.
	select {
	case <-ctx.Done():
		return
	case <-time.After(periodicReorgInterval / 2):
	}

	ticker := time.NewTicker(periodicReorgInterval)
	defer ticker.Stop()

	for {
		w.runPeriodicReorg(ctx)

		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
		}
	}
}

// runPeriodicReorg discovers active cubes and runs reorganizer for each.
func (w *Worker) runPeriodicReorg(ctx context.Context) {
	cubes := w.getActiveCubes(ctx)
	if len(cubes) == 0 {
		w.logger.Debug("scheduler: periodic reorg — no active cubes found")
		return
	}
	w.logger.Info("scheduler: periodic reorg starting",
		slog.Int("cubes", len(cubes)))

	archived := int64(0)
	for _, cubeID := range cubes {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// WM compaction: summarize old WM nodes → EpisodicMemory LTM before reorg.
		// Run before Run() so the compacted EpisodicMemory can be deduplicated too.
		if compacted, err := w.reorg.CompactWorkingMemory(ctx, cubeID); err != nil {
			w.logger.Debug("scheduler: wm compaction failed",
				slog.String("cube_id", cubeID), slog.Any("error", err))
		} else if compacted {
			w.logger.Info("scheduler: wm compacted",
				slog.String("cube_id", cubeID))
		}

		w.reorg.Run(ctx, cubeID)

		// D3 tree reorganizer — raw → episodic → semantic promotion. Gated by
		// MEMDB_REORG_HIERARCHY (default off). Non-fatal: errors inside
		// RunTreeReorgForCube are logged and the loop continues.
		if TreeHierarchyEnabled() {
			w.reorg.RunTreeReorgForCube(ctx, cubeID)
		}

		// Decay importance_score for all LTM/UserMemory of this cube.
		// Auto-archive memories that have faded below the threshold.
		n, err := w.reorg.DecayAndArchive(ctx, cubeID)
		if err != nil {
			w.logger.Debug("scheduler: importance decay failed",
				slog.String("cube_id", cubeID), slog.Any("error", err))
		} else {
			archived += n
		}
	}
	w.logger.Info("scheduler: periodic reorg complete",
		slog.Int("cubes", len(cubes)),
		slog.Int64("archived_by_decay", archived))
}

// getActiveCubes returns cube IDs by scanning VSET keys (wm:v:<cubeID>).
// Falls back to scanning scheduler stream keys if no VSET keys found.
func (w *Worker) getActiveCubes(ctx context.Context) []string {
	seen := make(map[string]bool)
	cubes := w.scanVSetCubeIDs(ctx, seen)
	if len(cubes) == 0 {
		cubes = w.scanStreamCubeIDs(ctx, seen)
	}
	return cubes
}

// scanVSetCubeIDs extracts cube IDs from VSET hot cache keys (wm:v:<cubeID>).
func (w *Worker) scanVSetCubeIDs(ctx context.Context, seen map[string]bool) []string {
	const vsetPrefix = "wm:v:"
	var cubes []string
	var cursor uint64
	for {
		batch, next, err := w.redis.Scan(ctx, cursor, vsetKeyScanPattern, scanBatchSize).Result()
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Debug("scheduler: scan vset keys error", slog.Any("error", err))
			}
			break
		}
		for _, key := range batch {
			cubeID := key[len(vsetPrefix):]
			if cubeID != "" && !seen[cubeID] {
				seen[cubeID] = true
				cubes = append(cubes, cubeID)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return cubes
}

// scanStreamCubeIDs extracts cube IDs from scheduler stream keys as a fallback.
func (w *Worker) scanStreamCubeIDs(ctx context.Context, seen map[string]bool) []string {
	var cubes []string
	for _, key := range w.scanStreamKeys(ctx) {
		parts := splitStreamKey(key)
		if parts.cubeID != "" && !seen[parts.cubeID] {
			seen[parts.cubeID] = true
			cubes = append(cubes, parts.cubeID)
		}
	}
	return cubes
}
