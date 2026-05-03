package handlers

import "context"

// add_fine_async.go — fire-and-forget background extractors invoked from the
// fine-add orchestrator. Split out of add_fine.go (M11 R1) so the F3 event
// extractor can plug into a single fan-out point and the F4 buffer flush can
// reuse the same trigger seam.
//
// Each extractor implementation lives in its own file:
//   - generateEpisodicSummary  → add_episodic.go
//   - generateSkillMemory      → add_skill.go
//   - generateToolTrajectory   → add_tool.go
//   - profiler.TriggerRefresh  → internal/scheduler (legacy summary refresh)
//   - triggerProfileExtract    → add_fine_profile.go (M10 Stream 2, has its
//     own bounded-concurrency semaphore — preserved untouched per M10 audit C3).
//
// triggerBackgroundExtractors is the single fan-out used by both
// nativeFineAddForCube and (eventually) runFinePipeline.

// extractorTriggerInput is the per-request payload passed into the fan-out.
// Kept private to the package; callers populate it from fullAddRequest or
// from the buffer flush context. factCount is sourced from the just-completed
// extract step so episodic summary can short-circuit on empty extractions.
// ReqCtx is the request context from which each extractor derives its own
// context.WithoutCancel background context. This preserves the OTel trace
// (pgxotel spans + slogh trace_id) across all fire-and-forget goroutines
// without inheriting the request cancellation signal.
type extractorTriggerInput struct {
	ReqCtx       context.Context
	CubeID       string
	UserID       string
	SessionID    string
	Conversation string
	Now          string
	// ObservationDate is the in-conversation date (YYYY-MM-DD) of the
	// source messages — threaded into every derived row so retrieval reads
	// the conversation timeline, not the ingest wall-clock. Empty means
	// the caller failed to plumb a chat_time through and the affected
	// derived extractors must skip rather than poison memory with NOW.
	ObservationDate string
	FactCount       int
	MessageCount    int
}

// triggerBackgroundExtractors runs the five fire-and-forget extractors that
// follow a successful fine-add. None of them block the caller; each one has
// its own internal admission control / no-op guards. Adding the F3 event
// extractor is a one-line change here.
//
// Extractor list (7 background extractors):
//  1. generateEpisodicSummary    — session-level recap node
//  2. generateSkillMemory        — code/task skill chunks
//  3. generateToolTrajectory     — tool-call traces
//  4. profiler.TriggerRefresh    — legacy unstructured profile summary
//  5. triggerProfileExtract      — structured user profile (M10 S2,
//                                  bounded by profileExtractSemaphore)
//  6. triggerEdgeInvalidationJudge — M11 F11 bi-temporal edge invalidator
//                                  (bounded by edgeJudgeSemaphore)
//  7. triggerEventExtract        — M11 F3 Memobase event extractor
//                                  (bounded by eventExtractSemaphore)
func (h *Handler) triggerBackgroundExtractors(in extractorTriggerInput) {
	if h == nil {
		return
	}
	// 1. Episodic summary — session-level overview node.
	h.generateEpisodicSummary(in.ReqCtx, in.CubeID, in.UserID, in.SessionID, in.Conversation, in.Now, in.ObservationDate, in.FactCount)

	// 2. Skill memory — extract reusable skill chunks from sufficiently long convs.
	h.generateSkillMemory(in.ReqCtx, in.CubeID, in.UserID, in.Conversation, in.MessageCount)

	// 3. Tool trajectory — capture tool-call traces if any are present.
	h.generateToolTrajectory(in.ReqCtx, in.CubeID, in.UserID, in.Conversation)

	// 4. Legacy profile summary refresh — runs only if the profiler is wired in.
	if h.profiler != nil {
		h.profiler.TriggerRefresh(in.CubeID)
	}

	// 5. Structured user-profile extraction — gated by MEMDB_PROFILE_EXTRACT and
	// bounded by profileExtractSemaphore (M10 audit C3 fix). Do NOT remove the
	// admission control: it caps goroutine count under burst load.
	h.triggerProfileExtract(in.ReqCtx, in.Conversation, in.UserID, in.CubeID)

	// 6. M11 F11 — bi-temporal edge invalidation judge. Re-judges entity
	// edges this /add just wrote against their currently-valid peers; on
	// confidence >= 0.7 flips invalid_at on superseded peers. Bounded by
	// edgeJudgeSemaphore; gated by MEMDB_F11_EDGE_JUDGE.
	h.triggerEdgeInvalidationJudge(in.ReqCtx, in.CubeID, in.Now)

	// 7. M11 F3 — Memobase event extraction. Produces per-blob event_summary
	// rows with [mention YYYY/MM/DD] anchors + tag list, persisted to
	// memos_graph.user_events for the search-time inject_events stage.
	// Bounded by eventExtractSemaphore; gated by MEMDB_F3_EVENTS.
	// The `in.Now` anchor is threaded through so EventExtractor.Extract
	// uses the request-time clock the rest of the /add pipeline saw,
	// instead of an unrelated time.Now() call.
	h.triggerEventExtract(in.ReqCtx, in.Conversation, in.UserID, in.CubeID, in.Now)
}
