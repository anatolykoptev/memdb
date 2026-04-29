package handlers

// chat_dual_speaker.go — M9 server-side dual-speaker fan-out for chat.
//
// Mirrors the eval-only client wrapper in evaluation/locomo/query.py
// (query_search_dual / _build_dual_speaker_system_prompt / query_chat_dual)
// on the server so vaelor and other prod consumers can opt in via the
// JSON `speakers` field instead of duplicating the harness's three-step
// loop (per-speaker search → label memories → per-speaker prompt block).
//
// Compatibility contract: when Speakers is empty (or len==1), nothing in
// this file runs — the legacy single-cube chat retrieval & prompt branch
// stays unchanged. Zero behaviour change for existing callers.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

const dualSpeakerSurfaceChat = "chat"

// chatRetrieve routes between the legacy single-speaker retrieval
// (chatSearchMemories) and the M9 dual-speaker fan-out
// (chatSearchMemoriesDual). The fourth return value is the per-speaker
// bucket BEFORE merge — non-nil only when the dual-speaker branch fires;
// callers use it to render the labelled prompt block. Nil for the legacy
// single-speaker path.
//
// Engagement metric is emitted here (once per chat call), so dashboards
// can split chat-with-fan-out vs chat-without by surface label.
func (h *Handler) chatRetrieve(
	ctx context.Context,
	req *nativeChatRequest,
) ([]map[string]any, string, []chatDualSpeakerLeg, error) {
	if len(req.Speakers) >= 2 {
		observability.RecordDualSpeakerEngaged(ctx, dualSpeakerSurfaceChat, len(req.Speakers))
		return h.chatSearchMemoriesDual(ctx, req)
	}
	observability.RecordDualSpeakerEngaged(ctx, dualSpeakerSurfaceChat, len(req.Speakers))
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
// LoCoMo harness (header + "## Speaker X memories" blocks) so the
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

// chatProfileUserID returns the user_id to use for profile-section lookup.
// Single-speaker requests use req.UserID directly. Dual-speaker requests
// without an explicit UserID skip profile injection (return "") because
// merging multiple speakers' profile rows into one prompt section would
// cross tenant boundaries (security audit C1 from chat_helpers.go:50).
func chatProfileUserID(req *nativeChatRequest) string {
	if req.UserID != nil && *req.UserID != "" {
		return *req.UserID
	}
	return ""
}

// chatDualSpeakerLeg holds one speaker's chat-side retrieval outcome.
type chatDualSpeakerLeg struct {
	speaker  string
	memories []map[string]any
	pref     string
	err      error
}

// chatSearchMemoriesDual fans out chat retrieval across req.Speakers in
// parallel and returns the merged memories + a labelled-block system
// prompt addition. Returns (memories, prefString, perSpeaker, error)
// where `perSpeaker` is the per-speaker bucket BEFORE merge — used by
// buildDualSpeakerPromptBlock to render "## Speaker X memories" sections.
//
// Search semantics per leg mirror chatSearchMemories: same threshold
// (req.Threshold || 0.30), same chatMinPersonalMem floor, same dedup
// resolution. Each leg's params are derived from the request with
// UserName/CubeID overridden to that speaker's id.
func (h *Handler) chatSearchMemoriesDual(
	ctx context.Context,
	req *nativeChatRequest,
) ([]map[string]any, string, []chatDualSpeakerLeg, error) {
	topK := search.DefaultTextTopK
	if req.TopK != nil {
		topK = *req.TopK
	}
	prefTopK := search.DefaultPrefTopK
	if req.PrefTopK != nil {
		prefTopK = *req.PrefTopK
	}
	topKPerSpeaker := topK
	if req.TopKPerSpeaker != nil && *req.TopKPerSpeaker > 0 {
		topKPerSpeaker = *req.TopKPerSpeaker
	}

	dedup := chatResolveDedup(req.Mode)
	level, err := parseChatLevel(req)
	if err != nil {
		return nil, "", nil, err
	}

	mergeStrategy := mergeStrategyDefault
	if req.MergeStrategy != nil && *req.MergeStrategy != "" {
		mergeStrategy = *req.MergeStrategy
	}

	legs := make([]chatDualSpeakerLeg, len(req.Speakers))
	t0 := time.Now()
	var wg sync.WaitGroup
	for i, sp := range req.Speakers {
		wg.Add(1)
		go func(idx int, speaker string) {
			defer wg.Done()
			params := search.SearchParams{
				Query:       *req.Query,
				UserName:    speaker,
				CubeID:      speaker,
				AgentID:     stringOrEmpty(req.AgentID),
				TopK:        topKPerSpeaker,
				PrefTopK:    prefTopK,
				IncludePref: derefBoolOr(req.IncludePreference, true),
				Dedup:       dedup,
				Level:       level,
				LLMRerank:   true,
			}
			out, lerr := h.searchService.SearchByLevel(ctx, params)
			if lerr != nil || out == nil || out.Result == nil {
				legs[idx] = chatDualSpeakerLeg{speaker: speaker, err: lerr}
				return
			}
			var mems []map[string]any
			if len(out.Result.TextMem) > 0 {
				mems = out.Result.TextMem[0].Memories
			}
			tagged := tagSpeakerLabel(mems, speaker)
			legs[idx] = chatDualSpeakerLeg{
				speaker:  speaker,
				memories: tagged,
				pref:     out.Result.PrefString,
			}
		}(i, sp)
	}
	wg.Wait()

	// Aggregate errors / partial-success.
	var firstErr error
	successCount := 0
	for _, l := range legs {
		if l.err != nil {
			if firstErr == nil {
				firstErr = l.err
			}
			h.logger.Warn("dual_speaker chat: leg failed",
				slog.String("speaker", l.speaker), slog.Any("error", l.err))
			continue
		}
		successCount++
	}
	if successCount == 0 && firstErr != nil {
		return nil, "", nil, firstErr
	}

	// Merge for the FLAT memory list passed to formatMemories. The labelled
	// per-speaker block in the system prompt comes from `legs` directly.
	results := make([]dualSpeakerSearchResult, len(legs))
	for i, l := range legs {
		results[i] = dualSpeakerSearchResult{
			speaker:  l.speaker,
			memories: l.memories,
			pref:     l.pref,
			err:      l.err,
		}
	}
	merged := mergeDualSpeakerResults(results, mergeStrategy, topK)

	// Threshold filter on the merged set (same shape as single-speaker path).
	threshold := 0.30
	if req.Threshold != nil {
		threshold = *req.Threshold
	}
	filtered := filterMemoriesByThreshold(merged, threshold, chatMinPersonalMem)

	observability.RecordDualSpeakerMerged(ctx, dualSpeakerSurfaceChat, len(merged))
	observability.RecordDualSpeakerLatency(ctx, dualSpeakerSurfaceChat,
		float64(time.Since(t0).Microseconds())/1000.0)

	pref := ""
	if len(legs) > 0 {
		pref = legs[0].pref
	}
	return filtered, pref, legs, nil
}

// buildDualSpeakerPromptBlock renders a "## Speaker <id> memories: ..."
// block per speaker. Mirrors the harness shape from
// evaluation/locomo/query.py:_build_dual_speaker_system_prompt but uses
// "## Speaker X" headers (the prod-friendly name) instead of "[speaker:X]".
//
// Empty legs (search failed or returned 0) render as "(no memories
// retrieved)" so the model sees the explicit absence — matches the
// harness's behaviour.
func buildDualSpeakerPromptBlock(legs []chatDualSpeakerLeg) string {
	if len(legs) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, l := range legs {
		sb.WriteString(fmt.Sprintf("## Speaker %s memories:\n", l.speaker))
		if l.err != nil || len(l.memories) == 0 {
			sb.WriteString("(no memories retrieved)\n\n")
			continue
		}
		for i, m := range l.memories {
			text, _ := m["memory"].(string)
			if text == "" {
				continue
			}
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// dualSpeakerChatPromptHeader is prepended to the dual-speaker system
// prompt. Echoes the harness header tone ("two-speaker conversation",
// "treat both speakers' evidence equally") but stays template-neutral so
// the existing factual-rules block from buildSystemPromptWithDecision can
// be appended after the speaker blocks.
const dualSpeakerChatPromptHeader = "You are a factual QA assistant answering a question about a recorded multi-speaker conversation. Below are memories retrieved separately from each speaker's personal memory store; treat every speaker's evidence as equally authoritative and combine across speakers when the question requires it."

// composeDualSpeakerSystemPrompt assembles the full system prompt for a
// dual-speaker chat call when the caller did NOT supply a custom
// system_prompt. Order: header → per-speaker blocks → factual rules
// (delegated to buildSystemPromptWithDecision via basePrompt route).
//
// The returned string is fed into buildSystemPromptWithDecision as
// basePrompt. We pre-compose the header + speaker blocks here so the
// decision-routing logic in chat_prompt.go can append the M12.4 anti-
// refusal rules block uniformly (same code path as the harness's
// `system_prompt + answer_style=factual` shape that already works in prod).
func composeDualSpeakerSystemPrompt(legs []chatDualSpeakerLeg) string {
	block := buildDualSpeakerPromptBlock(legs)
	if block == "" {
		return dualSpeakerChatPromptHeader
	}
	return dualSpeakerChatPromptHeader + "\n\n" + block
}
