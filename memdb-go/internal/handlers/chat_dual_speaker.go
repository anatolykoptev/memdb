package handlers

// chat_dual_speaker.go — M9 server-side dual-speaker fan-out for chat:
// ROUTER + types. Search fan-out logic lives in chat_dual_search.go;
// system-prompt assembly lives in chat_dual_prompt.go.
//
// Mirrors the eval-only client wrapper in evaluation/locomo/query.py
// (query_search_dual / _build_dual_speaker_system_prompt / query_chat_dual)
// on the server so vaelor and other prod consumers can opt in via the
// JSON `speakers` field instead of duplicating the harness's three-step
// loop (per-speaker search → label memories → per-speaker prompt block).
//
// Compatibility contract: when Speakers is empty (or len==1), the
// dual-speaker code path stays dormant — the legacy single-cube chat
// retrieval & prompt branch runs unchanged.

import (
	"context"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
)

// dualSpeakerSurfaceChat is the surface label used by the M9
// observability counters for dual-speaker chat-side operations.
const dualSpeakerSurfaceChat = "chat"

// chatDualSpeakerLeg holds one speaker's chat-side retrieval outcome.
//
// Lives in the router file (rather than chat_dual_search.go) because
// both the search path and the prompt path consume it — placing the
// type here keeps the dependency direction acyclic.
type chatDualSpeakerLeg struct {
	speaker  string
	memories []map[string]any
	pref     string
	err      error
}

// chatRetrieve routes between the legacy single-speaker retrieval
// (chatSearchMemories) and the M9 dual-speaker fan-out
// (chatSearchMemoriesDual). The fourth return value is the per-speaker
// bucket BEFORE merge — non-nil only when the dual-speaker branch
// fires; callers use it to render the labelled prompt block. Nil for
// the legacy single-speaker path.
//
// The engagement metric is emitted exactly once per call regardless of
// branch so dashboards can split chat-with-fan-out vs chat-without by
// surface label.
func (h *Handler) chatRetrieve(
	ctx context.Context,
	req *nativeChatRequest,
) ([]map[string]any, string, []chatDualSpeakerLeg, error) {
	observability.RecordDualSpeakerEngaged(ctx, dualSpeakerSurfaceChat, len(req.Speakers))
	if len(req.Speakers) >= 2 {
		return h.chatSearchMemoriesDual(ctx, req)
	}
	memories, pref, err := h.chatSearchMemories(ctx, req)
	return memories, pref, nil, err
}

// resolveBasePrompt picks the basePrompt argument for buildSystemPrompt.
// Three precedence levels:
//
//  1. caller-supplied SystemPrompt (non-empty)  — wins, legacy contract.
//  2. dual-speaker mode (legs != nil)           — server-built block.
//  3. neither                                   — empty string, the
//     buildSystemPromptWithDecision picks the conversational/factual
//     template based on answer_style.
//
// In case 2 the caller has explicitly opted in to server-side fan-out
// without supplying their own prompt — we render the same shape as the
// LoCoMo harness (header + Current time + per-speaker blocks) so the
// downstream factual-rules path in buildSystemPromptWithDecision can
// append the M12.4 anti-refusal block uniformly.
func (h *Handler) resolveBasePrompt(req *nativeChatRequest, dualLegs []chatDualSpeakerLeg) string {
	if req.SystemPrompt != nil && *req.SystemPrompt != "" {
		return *req.SystemPrompt
	}
	if len(dualLegs) > 0 {
		return composeDualSpeakerSystemPrompt(dualLegs)
	}
	return ""
}

// chatProfileUserID returns the user_id to use for profile-section
// lookup. Single-speaker requests use req.UserID directly. Dual-speaker
// requests without an explicit UserID skip profile injection (return
// "") because merging multiple speakers' profile rows into one prompt
// section would cross tenant boundaries (security audit C1 from
// chat_helpers.go:50).
//
// Note: scoped to dual-speaker because the security constraint only
// applies when the caller may have set Speakers; single-speaker callers
// always have UserID set.
func chatProfileUserID(req *nativeChatRequest) string {
	if req.UserID != nil && *req.UserID != "" {
		return *req.UserID
	}
	return ""
}
