package handlers

// chat_helpers.go — shared helpers for chat complete and stream handlers.

import (
	"context"
	"log/slog"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/search"
)

// chatSearchMemories runs the search pipeline for chat and returns filtered memories + pref string.
func (h *Handler) chatSearchMemories(ctx context.Context, req *nativeChatRequest) ([]map[string]any, string, error) {
	cubeIDs := req.ReadableCubeIDs
	if len(cubeIDs) == 0 && req.MemCubeID != nil && *req.MemCubeID != "" {
		cubeIDs = []string{*req.MemCubeID}
	} else if len(cubeIDs) == 0 {
		cubeIDs = []string{*req.UserID}
	}

	topK := search.DefaultTextTopK
	if req.TopK != nil {
		topK = *req.TopK
	}
	prefTopK := search.DefaultPrefTopK
	if req.PrefTopK != nil {
		prefTopK = *req.PrefTopK
	}

	params := search.SearchParams{
		Query:       *req.Query,
		UserName:    *req.UserID,
		CubeID:      cubeIDs[0],
		AgentID:     stringOrEmpty(req.AgentID),
		TopK:        topK,
		PrefTopK:    prefTopK,
		IncludePref: derefBoolOr(req.IncludePreference, true),
		Dedup:       "no",
	}

	output, err := h.searchService.Search(ctx, params)
	if err != nil {
		return nil, "", err
	}

	var memories []map[string]any
	if output.Result != nil && len(output.Result.TextMem) > 0 {
		memories = output.Result.TextMem[0].Memories
	}

	// Drop OuterMemory type memories from chat context.
	memories = filterOuterMemory(memories)

	threshold := 0.30
	if req.Threshold != nil {
		threshold = *req.Threshold
	}
	filtered := filterMemoriesByThreshold(memories, threshold, 3)

	prefString := ""
	if output.Result != nil {
		prefString = output.Result.PrefString
	}

	return filtered, prefString, nil
}

// filterOuterMemory removes OuterMemory type entries from the slice.
func filterOuterMemory(memories []map[string]any) []map[string]any {
	if len(memories) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(memories))
	for _, m := range memories {
		if memType(m) != "OuterMemory" {
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
			UserID:          &userID,
			AsyncMode:       &asyncMode,
			Messages:        []chatMessage{
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
