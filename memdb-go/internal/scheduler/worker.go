package scheduler

// worker.go — core: Worker struct, NewWorker, Run, Stop, constants.
//
// File layout:
//   worker.go                    — struct, constructor, Run/Stop, all constants
//   worker_stream.go            — readLoop, reclaimLoop + Redis stream helpers
//   worker_process.go           — processLoop, handle (message dispatch)
//   worker_periodic.go          — periodicReorgLoop, DLQ
//   worker_retry.go             — retryLoop, scheduleRetry (exponential backoff ZSet)
//   worker_retry_helpers.go     — go-redis ZSet helpers
//   reorganizer_retryable.go    — error-returning wrappers for retry-aware dispatch
//   worker_parsers.go           — pure parse helpers (no Redis dependency)

import (
"context"
"log/slog"
"time"

"github.com/redis/go-redis/v9"

"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
// consumerName identifies this worker instance inside the consumer group.
consumerName = "memdb_go_worker"

// readBatchSize is the max number of messages read per XREADGROUP call per stream.
readBatchSize = 10

// blockDuration is how long XREADGROUP blocks waiting for new messages.
// Kept short so total scan cycle (N streams × blockDuration) stays under scanInterval.
blockDuration = 100 * time.Millisecond

// scanInterval is how often the worker re-scans Redis for new stream keys.
// Kept short (2s) so async-submitted tasks are picked up quickly.
scanInterval = 2 * time.Second

// reclaimInterval is how often the worker checks for abandoned pending messages.
reclaimInterval = 30 * time.Second

// minIdleTime is the minimum time a pending message must be idle before
// XAUTOCLAIM reclaims it (mirrors Python's DEFAULT_PENDING_CLAIM_MIN_IDLE_MS = 1h).
minIdleTime = time.Hour

// highMsgChanBuffer is the size of the high-priority message channel.
// Smaller than low because high-priority messages are processed immediately.
highMsgChanBuffer = 32

// lowMsgChanBuffer is the size of the low-priority (background) message channel.
lowMsgChanBuffer = 128

// periodicReorgInterval is how often the worker runs the reorganizer for all
// active cubes regardless of incoming stream messages.
// Inspired by MemOS RedisStreamsScheduler periodic timer pattern.
// 6h balances freshness vs LLM cost (each cube = 1 FindNearDuplicates + N LLM calls).
periodicReorgInterval = 6 * time.Hour

// vsetKeyPrefix is duplicated here to scan active cubes without importing db package.
vsetKeyScanPattern = "wm:v:*"

// scanBatchSize is the max number of Redis keys returned per SCAN iteration.
// 200 is a safe value that balances iteration speed vs per-call latency.
scanBatchSize = 200
)

// streamMsg bundles a parsed message with its origin stream key.
type streamMsg struct {
	msg ScheduleMessage
}

// Worker is a Redis Streams consumer for MemDB background tasks.
// It runs four goroutines:
//   - readLoop:    scans for new stream keys, XREADGROUP new messages → highMsgCh or lowMsgCh
//   - reclaimLoop: XAUTOCLAIM abandoned pending messages → highMsgCh or lowMsgCh
//   - retryLoop:   polls ZSet for due retry tasks → highMsgCh or lowMsgCh
//   - processLoop: priority-aware dispatch — drains highMsgCh first, then lowMsgCh
//
// Optional goroutine (when pg is non-nil and MEMDB_PAGERANK_ENABLED=true):
//   - pageRankLoop: computes per-cube PageRank on memory_edges every 6h (M10 S7).
//
// Priority classification (see isHighPriority in message.go):
//   HIGH: mem_update, query, mem_feedback  — user-triggered, latency-sensitive
//   LOW:  mem_organize, mem_read, pref_add, add, answer — background work
//
// The worker uses its own consumer group (memdb_go_scheduler) which is independent
// from Python's scheduler_group — both consume all messages in parallel.
//
// TaskStatusTracker writes task lifecycle events to Redis Hash memos:task_meta:{user_id}
// using the same schema as Python — enabling the Python API's /scheduler/wait endpoints
// to observe Go-processed tasks.
type Worker struct {
	redis      *redis.Client
	reorg      *Reorganizer
	pg         *db.Postgres // optional; required for PageRank goroutine
	logger     *slog.Logger
	highMsgCh  chan streamMsg // high-priority: mem_update, query, mem_feedback
	lowMsgCh   chan streamMsg // low-priority:  mem_organize, mem_read, pref_add, add, answer
	stopCh     chan struct{}
	tracker    *TaskStatusTracker // writes memos:task_meta:{user_id} — shared with Python API
}

// NewWorker creates a Worker. reorg may be nil if no LLM/postgres is configured
// (mem_organize messages will be XACK'd without processing).
func NewWorker(rdb *redis.Client, reorg *Reorganizer, logger *slog.Logger) *Worker {
	return &Worker{
		redis:     rdb,
		reorg:     reorg,
		logger:    logger,
		highMsgCh: make(chan streamMsg, highMsgChanBuffer),
		lowMsgCh:  make(chan streamMsg, lowMsgChanBuffer),
		stopCh:    make(chan struct{}),
		tracker:   NewTaskStatusTracker(rdb),
	}
}

// SetPostgres wires a Postgres client into the Worker.
// Must be called before Run(). Enables the PageRank background goroutine.
func (w *Worker) SetPostgres(pg *db.Postgres) {
	w.pg = pg
}

// Run starts the worker goroutines and blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
w.logger.Info("scheduler worker: starting",
slog.String("consumer_group", ConsumerGroup),
slog.String("stream_prefix", StreamKeyPrefix),
)

go w.readLoop(ctx)
go w.reclaimLoop(ctx)
go w.retryLoop(ctx)
if w.reorg != nil {
go w.periodicReorgLoop(ctx)
}
// M10 Stream 7: PageRank background goroutine — requires Postgres + feature gate.
if w.pg != nil && pageRankEnabled() {
	go w.runPageRankLoop(ctx, w.pg)
	w.logger.Info("pagerank: background goroutine started",
		slog.Duration("interval", pageRankInterval()),
	)
}
w.processLoop(ctx) // blocks until ctx cancelled
}

// Stop signals the worker to stop. Blocks until internal channels are drained.
func (w *Worker) Stop() {
close(w.stopCh)
}
