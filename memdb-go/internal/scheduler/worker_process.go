package scheduler

// worker_process.go — message dispatch: processLoop and handle.
// Covers: processLoop, handle (full switch by msg.Label).

import (
	"context"
	"log/slog"
)

// handle dispatches a message to the appropriate handler.
// On transient error: scheduleRetry (exponential backoff → DLQ after maxRetries).
// On success or permanent skip: XACK immediately.
// Retry messages (RetryCount > 0) skip XACK since they were never in the PEL.
func (w *Worker) handle(ctx context.Context, msg ScheduleMessage) {
	log := w.logger.With(
		slog.String("label", msg.Label),
		slog.String("cube_id", msg.CubeID),
		slog.String("msg_id", msg.MsgID),
		slog.Int("retry_count", msg.RetryCount),
	)

	// Track task lifecycle in Redis (memos:task_meta:{user_id}) — same schema as Python.
	// Only track on first attempt (RetryCount == 0) to avoid duplicate submitted events.
	if msg.RetryCount == 0 {
		w.tracker.TaskSubmitted(ctx, msg)
	}
	w.tracker.TaskStarted(ctx, msg)

	var handleErr error

	switch msg.Label {

	// --- Handled natively in Go ---

	case LabelAdd:
		// Already executed by Go native add pipeline (add_fast.go / add_fine.go).
		// Nothing to do — just XACK to keep our PEL clean.
		log.Debug("scheduler: add — already handled by Go pipeline, acking")

	case LabelMemOrganize:
		// Triggers Memory Reorganizer: FindNearDuplicates → Union-Find → LLM merge.
		log.Info("scheduler: mem_organize — running reorganizer")
		if w.reorg != nil {
			if err := w.reorg.RunWithError(ctx, msg.CubeID); err != nil {
				handleErr = err
			}
		} else {
			log.Debug("scheduler: reorganizer not configured, skipping")
		}

	// --- Go-native handlers (full or partial) ---

	case LabelMemRead:
		// Go-native: parse WM IDs from content, LLM-enhance each into LTM facts, delete WM nodes.
		// Falls back to XACK-only when reorg is not configured (e.g. LLM not available).
		if w.reorg != nil && msg.Content != "" {
			if ids := parseMemReadIDs(msg.Content); len(ids) > 0 {
				log.Info("scheduler: mem_read — processing raw WM nodes",
					slog.Int("wm_ids", len(ids)))
				if err := w.reorg.ProcessRawMemoryWithError(ctx, msg.CubeID, ids); err != nil {
					handleErr = err
				}
			} else {
				log.Debug("scheduler: mem_read — no WM IDs parsed, acking")
			}
		} else {
			log.Debug("scheduler: mem_read — delegated to Python scheduler_group, acking")
		}

	case LabelMemUpdate:
		// WorkingMemory refresh by query: embed query → search LTM → add to VSET.
		// Mirrors Python's process_session_turn in GeneralScheduler.
		// msg.Content contains the raw query text from the user.
		if w.reorg != nil && msg.Content != "" {
			log.Debug("scheduler: mem_update — refreshing working memory")
			if err := w.reorg.RefreshWorkingMemoryWithError(ctx, msg.CubeID, msg.Content); err != nil {
				handleErr = err
			}
		} else {
			log.Debug("scheduler: mem_update — reorg not configured or empty content, acking")
		}

	case LabelPrefAdd:
		// Go-native: extract user preferences from conversation via LLM → store as UserMemory in Postgres.
		// Replaces Python's pref_mem service — no Qdrant dependency required.
		if w.reorg != nil && msg.Content != "" {
			if conv := parsePrefConversation(msg.Content); conv != "" {
				log.Info("scheduler: pref_add — extracting preferences")
				if err := w.reorg.ExtractAndStorePreferencesWithError(ctx, msg.CubeID, conv); err != nil {
					handleErr = err
				}
			} else {
				log.Debug("scheduler: pref_add — no conversation content, acking")
			}
		} else {
			log.Debug("scheduler: pref_add — reorg not configured, acking")
		}

	case LabelQuery:
		// Python logs the query then re-submits as mem_update (with same content).
		// We trigger WM refresh directly here — faster than waiting for the Python relay.
		// VAdd is idempotent (CAS) so the double-refresh from the subsequent mem_update is harmless.
		if w.reorg != nil && msg.Content != "" {
			log.Debug("scheduler: query — refreshing working memory (pre-emptive)")
			// query is best-effort: errors are logged but not retried (low priority).
			w.reorg.RefreshWorkingMemory(ctx, msg.CubeID, msg.Content)
		} else {
			log.Debug("scheduler: query — acking (reorg not configured)")
		}

	case LabelAnswer:
		// Logs assistant answer as addMessage event. Pure logging.
		log.Debug("scheduler: answer — delegated to Python scheduler_group, acking")

	case LabelMemFeedback:
		// Full Go-native: parse retrieved_memory_ids + feedback_content → LLM analysis
		// → UpdateMemoryNodeFull / DeleteByPropertyIDs. Falls back to RunTargeted on LLM error.
		if w.reorg != nil && msg.Content != "" {
			ids, feedbackContent := parseFeedbackPayload(msg.Content)
			if len(ids) > 0 {
				log.Info("scheduler: mem_feedback — full LLM processing",
					slog.Int("memory_ids", len(ids)))
				if err := w.reorg.ProcessFeedbackWithError(ctx, msg.CubeID, ids, feedbackContent); err != nil {
					handleErr = err
				}
			} else {
				log.Debug("scheduler: mem_feedback — no retrieved_memory_ids, acking")
			}
		} else {
			log.Debug("scheduler: mem_feedback — reorg not configured, acking")
		}

	default:
		// Unknown label — future Python labels or custom extensions.
		// XACK to prevent PEL accumulation.
		log.Debug("scheduler: unknown label — acking without processing",
			slog.String("label", msg.Label))
	}

	// On error: schedule retry (exponential backoff) or DLQ if exhausted.
	if handleErr != nil {
		log.Warn("scheduler: handler error",
			slog.Any("error", handleErr),
			slog.Int("retry_count", msg.RetryCount),
			slog.Int("max_retries", msg.maxRetries()),
		)
		w.scheduleRetry(ctx, msg, handleErr.Error())
		// Mark failed only when retries are exhausted (DLQ path).
		// While retrying, the task stays "in_progress" until final outcome.
		if msg.RetryCount >= msg.maxRetries() {
			w.tracker.TaskFailed(ctx, msg, handleErr.Error())
		}
	} else {
		w.tracker.TaskCompleted(ctx, msg)
	}

	// XACK only for original stream messages (RetryCount == 0 means from PEL).
	// Retry messages come from the ZSet, not the stream PEL — no XACK needed.
	if msg.RetryCount == 0 && msg.StreamKey != "" && msg.MsgID != "" {
		if err := w.redis.XAck(ctx, msg.StreamKey, ConsumerGroup, msg.MsgID).Err(); err != nil {
			log.Debug("scheduler: xack failed", slog.Any("error", err))
		}
	}
}
