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
	"os"
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

// extractMemoryTs returns the in-conversation date for a single memory,
// trying observation_date → chat_time → created_at → created_time → time
// → date. Mirrors evaluation/locomo/query.py::_extract_ts (the harness
// equivalent) so server-built dual-speaker prompts share the M12.1
// temporal anchor with the legacy harness-built path.
//
// Returns "" when no usable date is found — caller renders the memory
// without a ts: prefix in that case.
func extractMemoryTs(m map[string]any) string {
	md, _ := m["metadata"].(map[string]any)
	if md == nil {
		md = m
	}
	keys := [...]string{"observation_date", "chat_time", "created_at", "created_time", "time", "date"}
	for _, k := range keys {
		v, ok := md[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if len(s) >= 10 {
			return s[:10]
		}
		return s
	}
	return ""
}

// buildDualSpeakerPromptBlock renders a "## Speaker <id> memories: ..."
// block per speaker. Mirrors the harness shape from
// evaluation/locomo/query.py:_build_dual_speaker_system_prompt — each
// memory line is prefixed with its in-conversation ts (M12.1 temporal
// anchor) so the LLM resolves "yesterday"/"last week" against the
// conversation's own timeline, not server wall-clock.
//
// The maximum ts seen across all legs is returned alongside the block
// so the caller can render a "Current time:" header without re-walking
// the memories.
//
// Empty legs (search failed or returned 0) render as "(no memories
// retrieved)" so the model sees the explicit absence.
func buildDualSpeakerPromptBlock(legs []chatDualSpeakerLeg) (string, string) {
	if len(legs) == 0 {
		return "", ""
	}
	var sb strings.Builder
	maxTs := ""
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
			ts := extractMemoryTs(m)
			if ts != "" {
				sb.WriteString(fmt.Sprintf("%d. %s: %s\n", i+1, ts, text))
				if ts > maxTs {
					maxTs = ts
				}
			} else {
				sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n"), maxTs
}

// dualSpeakerChatPromptHeader is prepended to the dual-speaker system
// prompt. Echoes the harness header tone ("two-speaker conversation",
// "treat both speakers' evidence equally") but stays template-neutral so
// the existing factual-rules block from buildSystemPromptWithDecision can
// be appended after the speaker blocks.
const dualSpeakerChatPromptHeader = "You are a factual QA assistant answering a question about a recorded multi-speaker conversation. Below are memories retrieved separately from each speaker's personal memory store; treat every speaker's evidence as equally authoritative and combine across speakers when the question requires it."

// dualSpeakerNowOverrideEnv is the operator-pin env that forces a fixed
// "Current time:" anchor regardless of retrieved memories. Mirrors the
// LOCOMO_CONV_NOW harness override so replay/eval scenarios can bolt the
// reference time without writing it into every payload.
const dualSpeakerNowOverrideEnv = "MEMDB_DUAL_NOW_OVERRIDE"

// composeDualSpeakerSystemPrompt assembles the full system prompt for a
// dual-speaker chat call when the caller did NOT supply a custom
// system_prompt. Order: header → "Current time: <ts>" anchor →
// per-speaker blocks → factual rules (delegated to
// buildSystemPromptWithDecision via basePrompt route).
//
// The "Current time:" line resolves in priority:
//
//   1. MEMDB_DUAL_NOW_OVERRIDE env (operator pin),
//   2. max ts across retrieved memories (the conversation's own timeline),
//   3. omit the line entirely — never falls back to time.Now() (M11
//      regression vector documented in M12 RCA).
//
// The trailer instruction nudges the LLM to resolve relative phrases
// against per-memory ts: prefixes (M12.1 anti-poisoning).
func composeDualSpeakerSystemPrompt(legs []chatDualSpeakerLeg) string {
	block, maxTs := buildDualSpeakerPromptBlock(legs)
	convNow := strings.TrimSpace(os.Getenv(dualSpeakerNowOverrideEnv))
	if convNow == "" {
		convNow = maxTs
	}

	var sb strings.Builder
	sb.WriteString(dualSpeakerChatPromptHeader)
	if convNow != "" {
		sb.WriteString("\n\nCurrent time: ")
		sb.WriteString(convNow)
	}
	if block != "" {
		sb.WriteString("\n\n")
		sb.WriteString(block)
	}
	if convNow != "" || block != "" {
		sb.WriteString("\n\nEach memory line is prefixed with its in-conversation date (YYYY-MM-DD). " +
			"Resolve relative phrases like 'last week', 'yesterday', 'next month' against the dated memory, " +
			"NOT against the 'Current time' header. When asked WHEN an event happened, answer with the most " +
			"specific date or relative phrase present in the memories themselves.")
	}
	return sb.String()
}
