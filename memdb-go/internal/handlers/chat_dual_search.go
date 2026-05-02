package handlers

// chat_dual_search.go — dual-speaker SEARCH fan-out for chat.
//
// Single responsibility: given a multi-speaker chat request, run one
// per-speaker retrieval in parallel and return the merged + filtered
// memory list plus the per-speaker buckets (so the prompt builder can
// render labelled blocks).
//
// Bug fixes vs the previous monolithic version:
//   - Empty memories no longer trigger a wasted tagSpeakerLabel alloc.
//   - "no error but nil result" is now treated as a failure (was being
//     silently counted as success with empty memories).
//   - PrefString from any successful leg propagates (was: legs[0].pref
//     only — speaker_b's pref was dropped if speaker_a was empty).

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

// dualSpeakerLegParams captures the per-leg search inputs. Computed
// once from the request and reused for every speaker so the params
// slice is not duplicated 2-N times in goroutines.
type dualSpeakerLegParams struct {
	query          string
	agentID        string
	topKPerSpeaker int
	prefTopK       int
	includePref    bool
	dedup          string
	level          search.Level
}

// dualSpeakerMergeParams holds the post-fan-out aggregation knobs.
type dualSpeakerMergeParams struct {
	topK          int
	threshold     float64
	mergeStrategy string
}

// errDualLegEmptyResult signals that a leg returned no error but a nil
// result — the previous code path swallowed this case.
var errDualLegEmptyResult = errors.New("dual_speaker: leg returned nil result with no error")

// chatSearchMemoriesDual fans out chat retrieval across req.Speakers in
// parallel and returns the merged memories + a labelled-block system
// prompt addition.
//
// Returns (memories, prefString, perSpeaker, error) where `perSpeaker`
// is the per-speaker bucket BEFORE merge — used by the prompt builder
// to render "## Speaker X memories:" sections.
//
// Search semantics per leg mirror chatSearchMemories: same threshold
// (req.Threshold || 0.30), same chatMinPersonalMem floor, same dedup
// resolution. Each leg's params are derived from the request with
// UserName/CubeID overridden to that speaker's id.
func (h *Handler) chatSearchMemoriesDual(
	ctx context.Context,
	req *nativeChatRequest,
) ([]map[string]any, string, []chatDualSpeakerLeg, error) {
	legParams, mergeParams, err := resolveDualSpeakerParams(req)
	if err != nil {
		return nil, "", nil, err
	}

	t0 := time.Now()
	legs := h.runDualSpeakerLegs(ctx, req.Speakers, *req.Query, legParams)

	if firstErr, ok := dualLegsAllFailed(legs); ok {
		h.logDualLegFailures(legs)
		return nil, "", nil, firstErr
	}
	h.logDualLegFailures(legs)

	merged := mergeDualLegs(legs, mergeParams)
	filtered := filterMemoriesByThreshold(merged, mergeParams.threshold, chatMinPersonalMem())

	observability.RecordDualSpeakerMerged(ctx, dualSpeakerSurfaceChat, len(merged))
	observability.RecordDualSpeakerLatency(ctx, dualSpeakerSurfaceChat,
		float64(time.Since(t0).Microseconds())/1000.0)

	return filtered, firstNonEmptyPref(legs), legs, nil
}

// resolveDualSpeakerParams pulls per-leg + per-merge knobs out of the
// request, applying the same defaults as the single-speaker chat path.
// Separated so chatSearchMemoriesDual stays readable and the parser is
// unit-testable in isolation.
func resolveDualSpeakerParams(req *nativeChatRequest) (dualSpeakerLegParams, dualSpeakerMergeParams, error) {
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

	level, err := parseChatLevel(req)
	if err != nil {
		return dualSpeakerLegParams{}, dualSpeakerMergeParams{}, err
	}

	mergeStrategy := mergeStrategyDefault
	if req.MergeStrategy != nil && *req.MergeStrategy != "" {
		mergeStrategy = *req.MergeStrategy
	}

	threshold := chatDefaultThreshold()
	if req.Threshold != nil {
		threshold = *req.Threshold
	}

	return dualSpeakerLegParams{
			query:          *req.Query,
			agentID:        stringOrEmpty(req.AgentID),
			topKPerSpeaker: topKPerSpeaker,
			prefTopK:       prefTopK,
			includePref:    derefBoolOr(req.IncludePreference, true),
			dedup:          chatResolveDedup(req.Mode),
			level:          level,
		}, dualSpeakerMergeParams{
			topK:          topK,
			threshold:     threshold,
			mergeStrategy: mergeStrategy,
		}, nil
}

// runDualSpeakerLegs fans out one search.SearchByLevel per speaker in
// parallel and returns each leg's outcome (positional — index matches
// the input speakers slice).
func (h *Handler) runDualSpeakerLegs(
	ctx context.Context,
	speakers []string,
	query string,
	p dualSpeakerLegParams,
) []chatDualSpeakerLeg {
	legs := make([]chatDualSpeakerLeg, len(speakers))
	var wg sync.WaitGroup
	for i, sp := range speakers {
		wg.Add(1)
		go func(idx int, speaker string) {
			defer wg.Done()
			legs[idx] = h.runOneDualLeg(ctx, speaker, query, p)
		}(i, sp)
	}
	wg.Wait()
	return legs
}

// runOneDualLeg executes a single per-speaker search and packages the
// outcome into a chatDualSpeakerLeg. Treats nil-result-without-error
// as a failure (previously silently produced empty memories that
// counted as success). Skips tagSpeakerLabel allocation on empty mems.
func (h *Handler) runOneDualLeg(
	ctx context.Context,
	speaker string,
	query string,
	p dualSpeakerLegParams,
) chatDualSpeakerLeg {
	params := search.SearchParams{
		Query:       query,
		UserName:    speaker,
		CubeID:      speaker,
		AgentID:     p.agentID,
		TopK:        p.topKPerSpeaker,
		PrefTopK:    p.prefTopK,
		IncludePref: p.includePref,
		Dedup:       p.dedup,
		Level:       p.level,
		LLMRerank:   true,
	}
	out, err := h.searchService.SearchByLevel(ctx, params)
	if err != nil {
		return chatDualSpeakerLeg{speaker: speaker, err: err}
	}
	if out == nil || out.Result == nil {
		return chatDualSpeakerLeg{speaker: speaker, err: errDualLegEmptyResult}
	}
	var mems []map[string]any
	if len(out.Result.TextMem) > 0 {
		mems = out.Result.TextMem[0].Memories
	}
	tagged := mems
	if len(mems) > 0 {
		tagged = tagSpeakerLabel(mems, speaker)
	}
	return chatDualSpeakerLeg{
		speaker:  speaker,
		memories: tagged,
		pref:     out.Result.PrefString,
	}
}

// dualLegsAllFailed reports whether every leg returned an error.
// Returns (firstErr, true) iff no leg succeeded — caller propagates.
// Otherwise (nil, false) and the caller proceeds with partial results.
func dualLegsAllFailed(legs []chatDualSpeakerLeg) (error, bool) {
	var firstErr error
	for _, l := range legs {
		if l.err == nil {
			return nil, false
		}
		if firstErr == nil {
			firstErr = l.err
		}
	}
	return firstErr, firstErr != nil
}

// logDualLegFailures emits one Warn per failed leg so partial-success
// runs surface in operator logs without aborting the request.
func (h *Handler) logDualLegFailures(legs []chatDualSpeakerLeg) {
	for _, l := range legs {
		if l.err == nil {
			continue
		}
		h.logger.Warn("dual_speaker chat: leg failed",
			slog.String("speaker", l.speaker), slog.Any("error", l.err))
	}
}

// mergeDualLegs converts the per-speaker buckets into the flat list
// that downstream callers (formatMemories, threshold filter) expect.
func mergeDualLegs(legs []chatDualSpeakerLeg, p dualSpeakerMergeParams) []map[string]any {
	results := make([]dualSpeakerSearchResult, len(legs))
	for i, l := range legs {
		results[i] = dualSpeakerSearchResult(l)
	}
	return mergeDualSpeakerResults(results, p.mergeStrategy, p.topK)
}

// firstNonEmptyPref returns the first non-empty PrefString across legs.
// Previously the code returned legs[0].pref unconditionally, so the
// second speaker's pref was lost when speaker_a returned empty.
//
// This is intentionally first-wins (not concat) because the chat path
// passes a single PrefString into one prompt section — concatenation
// would risk merging incompatible per-speaker preferences. If a future
// caller needs both, expose a per-speaker pref slice instead.
func firstNonEmptyPref(legs []chatDualSpeakerLeg) string {
	for _, l := range legs {
		if l.pref != "" {
			return l.pref
		}
	}
	return ""
}
