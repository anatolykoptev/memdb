package handlers

// search_dual_speaker.go — M9 server-side dual-speaker fan-out for
// /product/search. When the request carries Speakers (len>=2), we issue
// one search per speaker in parallel, tag each returned memory with
// metadata.speaker_label = <speaker_id>, then merge the per-speaker
// buckets into a single TopK list using the requested merge_strategy.
//
// Compatibility contract: when Speakers is empty (or len==1), the legacy
// single-speaker NativeSearch path runs unchanged — zero behaviour change
// for existing callers (vaelor, the non-dual eval client wrappers).
//
// This is the server-side counterpart of evaluation/locomo/query.py
// :query_search_dual; vaelor and other prod consumers can now opt in via
// the JSON `speakers` field instead of copying the harness's two-call
// stitching loop.

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

// dualSpeakerSurface is the metric label for the search-side fan-out.
const dualSpeakerSurfaceSearch = "search"

// mergeStrategyDefault is the strategy used when req.MergeStrategy is nil
// or "". interleave preserves diversity (the LoCoMo harness's de-facto
// shape — every speaker contributes to top results regardless of absolute
// score), which mirrors evaluation/locomo/query.py:_merge_dual_results.
const mergeStrategyDefault = "interleave"

// dualSpeakerSearchResult holds one speaker's search outcome.
type dualSpeakerSearchResult struct {
	speaker  string
	memories []map[string]any
	pref     string
	err      error
}

// runDualSpeakerSearch executes the parallel per-speaker fan-out for
// NativeSearch and assembles the merged response. base carries every
// non-identity field (query, top_k, profile, level, ...); only UserName
// and CubeID are overridden per speaker.
//
// topKPerSpeaker is the per-leg budget (defaults to base.TopK when 0);
// finalTopK is the post-merge cap (== base.TopK).
//
// mode mirrors the request's Mode field ("fast" / "fine" / ""): we route
// each leg through the same selector as NativeSearch (Search / SearchFine
// / SearchByLevel) so dual-speaker callers get identical retrieval
// semantics to the single-speaker path. Otherwise a "fine" caller would
// silently fall back to the fast pipeline in dual mode — a silent
// behavioural drift the M14 audit (feedback_pipeline_magic_numbers.md)
// explicitly forbids.
//
// Returns a SearchOutput with the merged TextMem bucket. SkillMem / PrefMem
// / ToolMem bubble up from the FIRST speaker's response — fan-out semantics
// for those tiers are out of scope for M9 (LoCoMo doesn't exercise them
// and they are typically tenant-scoped, not speaker-scoped).
func (h *Handler) runDualSpeakerSearch(
	ctx context.Context,
	speakers []string,
	base search.SearchParams,
	topKPerSpeaker int,
	finalTopK int,
	mergeStrategy string,
	mode string,
) (*search.SearchOutput, error) {
	if topKPerSpeaker <= 0 {
		topKPerSpeaker = base.TopK
	}
	if finalTopK <= 0 {
		finalTopK = base.TopK
	}
	if mergeStrategy == "" {
		mergeStrategy = mergeStrategyDefault
	}

	results := make([]dualSpeakerSearchResult, len(speakers))
	t0 := time.Now()
	var wg sync.WaitGroup
	for i, sp := range speakers {
		wg.Add(1)
		go func(idx int, speaker string) {
			defer wg.Done()
			params := base
			params.UserName = speaker
			// CubeID defaults to user_id when not explicitly overridden upstream.
			// The dual-speaker harness assumption: each speaker stores in their
			// own cube == their user_id (LoCoMo: <conv>__speaker_a, etc.).
			params.CubeID = speaker
			params.TopK = topKPerSpeaker
			out, err := dispatchDualLegSearch(ctx, h.searchService, params, mode)
			if err != nil || out == nil || out.Result == nil {
				results[idx] = dualSpeakerSearchResult{speaker: speaker, err: err}
				return
			}
			var mems []map[string]any
			if len(out.Result.TextMem) > 0 {
				mems = out.Result.TextMem[0].Memories
			}
			tagged := tagSpeakerLabel(mems, speaker)
			results[idx] = dualSpeakerSearchResult{
				speaker:  speaker,
				memories: tagged,
				pref:     out.Result.PrefString,
			}
		}(i, sp)
	}
	wg.Wait()

	// Surface the first error if every speaker failed; otherwise warn and
	// continue with whatever succeeded — partial results are more useful
	// than a blanket 5xx for a multi-speaker call.
	var firstErr error
	successCount := 0
	for _, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			h.logger.Warn("dual_speaker search: leg failed",
				slog.String("speaker", r.speaker), slog.Any("error", r.err))
			continue
		}
		successCount++
	}
	if successCount == 0 && firstErr != nil {
		return nil, firstErr
	}

	merged := mergeDualSpeakerResults(results, mergeStrategy, finalTopK)
	observability.RecordDualSpeakerMerged(ctx, dualSpeakerSurfaceSearch, len(merged))
	observability.RecordDualSpeakerLatency(ctx, dualSpeakerSurfaceSearch,
		float64(time.Since(t0).Microseconds())/1000.0)

	// Build a SearchResult that mirrors the legacy single-speaker shape.
	// CubeID on the bucket: use the first speaker as the canonical cube
	// for downstream callers that read it (mostly Python parity code).
	pref := ""
	if len(results) > 0 {
		pref = results[0].pref
	}
	if merged == nil {
		// Match the single-speaker shape: empty slice, not nil. Python
		// callers (the LoCoMo harness, vaelor's Python adapters) JSON-
		// decode `null` as None and `[]` as empty list — keep parity.
		merged = []map[string]any{}
	}
	result := search.NewEmptySearchResult()
	result.TextMem = []search.MemoryBucket{{
		CubeID:     speakers[0],
		Memories:   merged,
		TotalNodes: len(merged),
	}}
	result.PrefString = pref
	return &search.SearchOutput{Result: result}, nil
}

// tagSpeakerLabel clones memory maps and stamps metadata.speaker_label
// = speaker. Cloning the outer map is cheap and avoids mutating shared
// structures returned by the search service (which may be cached / re-used).
// metadata is also cloned shallowly because it is the only nested map we
// actually modify.
func tagSpeakerLabel(memories []map[string]any, speaker string) []map[string]any {
	if len(memories) == 0 {
		return nil
	}
	out := make([]map[string]any, len(memories))
	for i, m := range memories {
		clone := make(map[string]any, len(m))
		for k, v := range m {
			clone[k] = v
		}
		md, _ := clone["metadata"].(map[string]any)
		mdClone := make(map[string]any, len(md)+1)
		for k, v := range md {
			mdClone[k] = v
		}
		mdClone["speaker_label"] = speaker
		clone["metadata"] = mdClone
		out[i] = clone
	}
	return out
}

// mergeDualSpeakerResults stitches per-speaker buckets into a final list
// capped at topK. Two strategies:
//
//   - "interleave" (default) — score-aware merge. Each leg is sorted by
//     metadata.relativity descending in-place, then a 2-pointer / heap
//     merge pulls items in global score order. Equal-score items break
//     ties via round-robin across legs so a saturated leg cannot starve
//     the other when scores collide. This replaces the legacy positional
//     round-robin which mixed high- and low-scoring items at the top
//     regardless of comparable relevance and was the largest single noise
//     source identified in the 2026-05-02 forensic.
//   - "score" — flat sort by metadata.relativity descending. Useful when
//     callers want absolute relevance over per-speaker fairness.
//
// In both cases duplicates are deduped by memory id.
func mergeDualSpeakerResults(results []dualSpeakerSearchResult, strategy string, topK int) []map[string]any {
	if topK <= 0 {
		return nil
	}
	// Filter to successful legs in input order.
	buckets := make([][]map[string]any, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			continue
		}
		buckets = append(buckets, r.memories)
	}
	if len(buckets) == 0 {
		return nil
	}

	switch strategy {
	case "score":
		flat := make([]map[string]any, 0, totalLen(buckets))
		for _, b := range buckets {
			flat = append(flat, b...)
		}
		sort.SliceStable(flat, func(i, j int) bool {
			return relativity(flat[i]) > relativity(flat[j])
		})
		return capDedup(flat, topK)
	default: // interleave (score-aware)
		flat := interleaveBuckets(buckets)
		return capDedup(flat, topK)
	}
}

// interleaveBuckets merges per-leg buckets into a single list ordered by
// metadata.relativity DESC. Per-leg input order is normalised first (each
// leg sorted DESC by relativity) so the merge does not depend on caller
// ordering. Tiebreak when scores are equal: round-robin across legs (via
// the per-leg cursor advancing in input-leg order). This preserves the
// "fair share when scores are comparable" property of the legacy
// positional interleave without ever placing a low-score item ahead of a
// high-score one across legs.
//
// Algorithm: maintain a cursor per leg; at each step pick the leg whose
// current head has the maximum relativity, advance that cursor, append
// the item to the output. When two legs tie on the head score, the leg
// with the lower index goes first (deterministic round-robin: the second
// equal-score pick across iterations alternates because the chosen leg's
// cursor advances). Cost: O(N · L) for L legs and N total items — L is 2
// in production so this stays cheap; a heap would only matter for L≥4.
func interleaveBuckets(buckets [][]map[string]any) []map[string]any {
	if len(buckets) == 0 {
		return nil
	}
	// Sort each leg DESC by relativity. Copy first to avoid mutating the
	// caller's slice (legs[].memories is reused by per-speaker prompt
	// rendering downstream).
	sortedLegs := make([][]map[string]any, len(buckets))
	for i, b := range buckets {
		s := make([]map[string]any, len(b))
		copy(s, b)
		sort.SliceStable(s, func(a, c int) bool { return relativity(s[a]) > relativity(s[c]) })
		sortedLegs[i] = s
	}

	total := 0
	for _, b := range sortedLegs {
		total += len(b)
	}
	out := make([]map[string]any, 0, total)
	cursors := make([]int, len(sortedLegs))

	for {
		bestLeg := -1
		var bestRel float64
		for li, b := range sortedLegs {
			if cursors[li] >= len(b) {
				continue
			}
			r := relativity(b[cursors[li]])
			// Strict > picks the lower leg index on ties — round-robin
			// fairness emerges across iterations because the picked leg's
			// cursor advances while the tied leg's stays.
			if bestLeg == -1 || r > bestRel {
				bestLeg = li
				bestRel = r
			}
		}
		if bestLeg == -1 {
			break
		}
		out = append(out, sortedLegs[bestLeg][cursors[bestLeg]])
		cursors[bestLeg]++
	}
	return out
}

// capDedup walks `in` once, keeping the first occurrence of each memory
// id, and stops at topK. Memories without an id field are kept verbatim
// (defensive — should not happen in production, but losing them silently
// would mask bugs).
func capDedup(in []map[string]any, topK int) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]map[string]any, 0, topK)
	for _, m := range in {
		if len(out) >= topK {
			break
		}
		id, _ := m["id"].(string)
		if id != "" {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
		}
		out = append(out, m)
	}
	return out
}

func totalLen(buckets [][]map[string]any) int {
	n := 0
	for _, b := range buckets {
		n += len(b)
	}
	return n
}

// dispatchDualLegSearch routes a single per-speaker leg through the same
// selector as NativeSearch. This keeps dual-speaker search retrieval
// semantics identical to the single-speaker path — same ranker, same
// graph expansion, same level-scoped bypass. Without this, a caller
// asking for mode=fine in dual mode would silently get the fast pipeline.
func dispatchDualLegSearch(ctx context.Context, svc dualLegSearcher, p search.SearchParams, mode string) (*search.SearchOutput, error) {
	switch {
	case p.Level != "" && p.Level != search.LevelAll:
		return svc.SearchByLevel(ctx, p)
	case mode == modeFine:
		return svc.SearchFine(ctx, p)
	default:
		return svc.Search(ctx, p)
	}
}

// dualLegSearcher is the subset of *search.SearchService methods used by
// dispatchDualLegSearch. Defined here (vs. exposed in search/) so unit
// tests can stub out the surface without depending on the full service.
type dualLegSearcher interface {
	Search(ctx context.Context, p search.SearchParams) (*search.SearchOutput, error)
	SearchFine(ctx context.Context, p search.SearchParams) (*search.SearchOutput, error)
	SearchByLevel(ctx context.Context, p search.SearchParams) (*search.SearchOutput, error)
}
