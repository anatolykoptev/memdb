package scheduler

// worker_priority.go — priority-aware message routing and processing.
//
// Design: single readLoop → two channels (highMsgCh / lowMsgCh) → single processLoop.
//
// Why two channels instead of two workers (MemOS approach):
//   - No goroutine duplication — one processLoop handles both priorities
//   - True priority guarantee: processLoop always drains highMsgCh before lowMsgCh
//   - Simpler retry/XACK logic — no cross-worker coordination needed
//   - Lower memory footprint — one consumer group, one PEL
//
// Priority select pattern (Go classic):
//   1. Non-blocking drain of highMsgCh (select with default)
//   2. If empty: blocking select on both channels (high wins on simultaneous arrival)
//
// This guarantees high-priority messages are never starved by a flood of low-priority ones,
// while low-priority messages are still processed when no high-priority work is pending.

import (
	"context"
)

// enqueue routes a streamMsg to the appropriate priority channel.
// Returns ctx.Err() if the context is cancelled while waiting to enqueue.
func (w *Worker) enqueue(ctx context.Context, sm streamMsg) error {
	if sm.msg.HighPriority {
		select {
		case w.highMsgCh <- sm:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case w.lowMsgCh <- sm:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processLoop reads from highMsgCh and lowMsgCh with priority ordering.
//
// Algorithm:
//  1. Non-blocking check of highMsgCh — process immediately if available.
//  2. Blocking select on both channels — highMsgCh wins on simultaneous arrival.
//
// This ensures high-priority messages (mem_update, query, mem_feedback) are
// always processed before background tasks (mem_organize, mem_read, pref_add).
func (w *Worker) processLoop(ctx context.Context) {
	for {
		// Phase 1: drain all pending high-priority messages first (non-blocking).
		for {
			select {
			case sm := <-w.highMsgCh:
				w.handle(ctx, sm.msg)
				continue
			default:
			}
			break
		}

		// Phase 2: blocking wait — high-priority wins on simultaneous arrival.
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case sm := <-w.highMsgCh:
			w.handle(ctx, sm.msg)
		case sm := <-w.lowMsgCh:
			// Before processing low-priority, do one more non-blocking check
			// in case a high-priority message arrived while we were waiting.
			select {
			case hi := <-w.highMsgCh:
				// Re-enqueue the low-priority message (non-blocking, channel has capacity).
				select {
				case w.lowMsgCh <- sm:
				default:
					// Channel full — process low-priority now to avoid deadlock.
					// This is a rare edge case under extreme load.
					w.logger.Debug("scheduler: low-priority channel full during priority swap, processing low first")
					w.handle(ctx, sm.msg)
				}
				w.handle(ctx, hi.msg)
			default:
				w.handle(ctx, sm.msg)
			}
		}
	}
}

// priorityLabel returns a human-readable priority label for logging.
func priorityLabel(high bool) string {
	if high {
		return "high"
	}
	return "low"
}
