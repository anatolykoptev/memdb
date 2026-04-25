package handlers

// chat_helpers.go — shared helpers for chat complete and stream handlers.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

const (
	memTypeOuter       = "OuterMemory" // memory type for internet-sourced memories
	chatMinPersonalMem = 3             // minimum personal memories to keep after threshold filtering
)

// parseChatLevel parses the level field from a chat request.
// Returns LevelAll + nil for omitted/empty; error for invalid values.
func parseChatLevel(req *nativeChatRequest) (search.Level, error) {
	if req.Level == nil || *req.Level == "" {
		return search.LevelAll, nil
	}
	return search.ParseLevel(*req.Level)
}

// chatSearchMemories runs the search pipeline for chat and returns filtered memories + pref string.
// Searches all readable cubes in parallel and merges results.
func (h *Handler) chatSearchMemories(ctx context.Context, req *nativeChatRequest) ([]map[string]any, string, error) {
	cubeIDs := resolveCubeIDs(req.ReadableCubeIDs, req.MemCubeID, req.UserID)

	topK := search.DefaultTextTopK
	if req.TopK != nil {
		topK = *req.TopK
	}
	prefTopK := search.DefaultPrefTopK
	if req.PrefTopK != nil {
		prefTopK = *req.PrefTopK
	}

	dedup := chatResolveDedup(req.Mode)

	level, err := parseChatLevel(req)
	if err != nil {
		return nil, "", err
	}

	baseParams := search.SearchParams{
		Query:       *req.Query,
		UserName:    *req.UserID,
		AgentID:     stringOrEmpty(req.AgentID),
		TopK:        topK,
		PrefTopK:    prefTopK,
		IncludePref: derefBoolOr(req.IncludePreference, true),
		Dedup:       dedup,
		Level:       level,
	}

	memories, prefString, err := h.searchAcrossCubes(ctx, cubeIDs, baseParams)
	if err != nil {
		return nil, "", err
	}

	// Drop OuterMemory unless internet search was requested.
	if !derefBoolOr(req.InternetSearch, false) {
		memories = filterOuterMemory(memories)
	}

	threshold := 0.30
	if req.Threshold != nil {
		threshold = *req.Threshold
	}
	filtered := filterMemoriesByThreshold(memories, threshold, chatMinPersonalMem)

	// Post-retrieval enhancement: disambiguate pronouns, resolve relative times, merge related.
	enhanced := search.EnhanceMemories(ctx, *req.Query, filtered, h.searchService.Enhance)

	return enhanced, prefString, nil
}

// chatResolveDedup determines the dedup mode for chat search.
// Applies the same three-tier logic as the search handler:
// default ("no") < profile override < request override.
func chatResolveDedup(mode *string) string {
	dedup := "no"
	if mode == nil || *mode == "" {
		return dedup
	}
	prof, err := search.LookupProfile(*mode)
	if err != nil {
		return dedup
	}
	if prof.Dedup != nil {
		dedup = *prof.Dedup
	}
	return dedup
}

// cubeResult holds the outcome of a single cube search.
type cubeResult struct {
	memories []map[string]any
	pref     string
	err      error
}

// searchAcrossCubes searches all cubes in parallel and merges results.
// For a single cube, no goroutines are spawned.
func (h *Handler) searchAcrossCubes(ctx context.Context, cubeIDs []string, base search.SearchParams) ([]map[string]any, string, error) {
	if len(cubeIDs) == 1 {
		return h.searchSingleCube(ctx, cubeIDs[0], base)
	}

	results := make([]cubeResult, len(cubeIDs))
	var wg sync.WaitGroup
	for i, cid := range cubeIDs {
		wg.Add(1)
		go func(idx int, cubeID string) {
			defer wg.Done()
			mems, pref, err := h.searchSingleCube(ctx, cubeID, base)
			results[idx] = cubeResult{memories: mems, pref: pref, err: err}
		}(i, cid)
	}
	wg.Wait()

	return h.mergeCubeResults(results)
}

// mergeCubeResults deduplicates and merges results from multiple cube searches.
func (h *Handler) mergeCubeResults(results []cubeResult) ([]map[string]any, string, error) {
	seen := make(map[string]struct{})
	var merged []map[string]any
	var prefParts []string
	var firstErr error

	for _, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			h.logger.Warn("chat multi-cube search: cube failed", slog.Any("error", r.err))
			continue
		}
		for _, m := range r.memories {
			id, _ := m["id"].(string)
			if id != "" {
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
			}
			merged = append(merged, m)
		}
		if r.pref != "" {
			prefParts = append(prefParts, r.pref)
		}
	}

	if len(merged) == 0 && firstErr != nil {
		return nil, "", firstErr
	}

	sortByRelativity(merged)

	pref := ""
	if len(prefParts) > 0 {
		pref = prefParts[0] // use first cube's preferences as primary
	}

	return merged, pref, nil
}

// searchSingleCube runs a search against one cube and extracts memories + pref string.
func (h *Handler) searchSingleCube(ctx context.Context, cubeID string, base search.SearchParams) ([]map[string]any, string, error) {
	params := base
	params.CubeID = cubeID

	output, err := h.searchService.SearchByLevel(ctx, params)
	if err != nil {
		return nil, "", err
	}

	var memories []map[string]any
	if output.Result != nil && len(output.Result.TextMem) > 0 {
		memories = output.Result.TextMem[0].Memories
	}

	prefString := ""
	if output.Result != nil {
		prefString = output.Result.PrefString
	}

	return memories, prefString, nil
}

// resolveCubeIDs returns the list of cube IDs to search, falling back to user_id.
func resolveCubeIDs(readableCubeIDs []string, memCubeID, userID *string) []string {
	if len(readableCubeIDs) > 0 {
		return readableCubeIDs
	}
	if memCubeID != nil && *memCubeID != "" {
		return []string{*memCubeID}
	}
	return []string{*userID}
}

// filterOuterMemory removes OuterMemory type entries from the slice.
func filterOuterMemory(memories []map[string]any) []map[string]any {
	if len(memories) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(memories))
	for _, m := range memories {
		if memType(m) != memTypeOuter {
			out = append(out, m)
		}
	}
	return out
}

// chatBuildMessages assembles the LLM message array from prompt, history, and query.
func chatBuildMessages(systemPrompt, query string, history []map[string]string) []map[string]string {
	msgs := make([]map[string]string, 0, 2+len(history))
	msgs = append(msgs, map[string]string{"role": "system", "content": systemPrompt})

	// Keep last chatMaxHistory messages from history.
	h := history
	if len(h) > chatMaxHistory {
		h = h[len(h)-chatMaxHistory:]
	}
	msgs = append(msgs, h...)
	msgs = append(msgs, map[string]string{"role": "user", "content": query})
	return msgs
}

// chatPostAdd fires a fire-and-forget add for the chat Q&A pair.
func (h *Handler) chatPostAdd(req *nativeChatRequest, query, response string) {
	userID := *req.UserID
	cubeIDs := req.WritableCubeIDs
	if len(cubeIDs) == 0 && req.MemCubeID != nil && *req.MemCubeID != "" {
		cubeIDs = []string{*req.MemCubeID}
	} else if len(cubeIDs) == 0 {
		cubeIDs = []string{userID}
	}
	sessionID := stringOrEmpty(req.SessionID)
	if sessionID == "" {
		sessionID = "default_session"
	}

	go func() {
		now := time.Now().Format("2006-01-02 15:04:05")
		asyncMode := modeAsync
		addReq := &fullAddRequest{
			UserID:    &userID,
			AsyncMode: &asyncMode,
			Messages: []chatMessage{
				{Role: "user", Content: query, ChatTime: now},
				{Role: "assistant", Content: response, ChatTime: now},
			},
			WritableCubeIDs: cubeIDs,
			SessionID:       &sessionID,
		}
		for _, cid := range cubeIDs {
			if _, err := h.nativeAddForCube(context.Background(), addReq, cid); err != nil {
				h.logger.Warn("chat post-add failed",
					slog.String("cube_id", cid), slog.Any("error", err))
			}
		}
	}()
}
