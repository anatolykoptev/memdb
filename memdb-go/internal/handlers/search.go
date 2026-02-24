package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/MemDBai/MemDB/memdb-go/internal/search"
)

const logQueryTruncLen = 60 // max chars for query logging

// NativeSearch handles POST /product/search with native Go implementation.
// Falls back to Python proxy when:
//   - searchService is nil or cannot search
//   - mode is "fine" (needs LLM)
//   - internet_search is true (needs SearXNG)
//   - any error during native search
func (h *Handler) NativeSearch(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req searchRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if !h.checkErrors(w, validateSearchRequest(req)) {
		return
	}

	normalized := normalizeSearch(body)

	// Proxy fallback conditions
	if h.searchService == nil || !h.searchService.CanSearch() {
		h.logger.Debug("native search: service unavailable, proxying")
		h.proxyWithBody(w, r, normalized)
		return
	}
	if req.Mode != nil && *req.Mode == modeFine {
		h.logger.Debug("native search: fine mode, proxying")
		h.proxyWithBody(w, r, normalized)
		return
	}
	if req.InternetSearch != nil && *req.InternetSearch {
		h.logger.Debug("native search: internet_search=true, proxying")
		h.proxyWithBody(w, r, normalized)
		return
	}

	params, err := buildSearchParams(req)
	if err != nil {
		h.writeValidationError(w, []string{err.Error()})
		return
	}
	ctx := r.Context()

	// Check cache
	profileKey := derefStringOr(req.Profile, "default")
	cacheKey := fmt.Sprintf("%ssearch:%s:%s:%s:%d:%d:%d:%s",
		cachePrefix, profileKey, params.UserName, hashQuery(params.Query), params.TopK, params.SkillTopK, params.PrefTopK, params.Dedup)
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cached)
		return
	}

	output, err := h.searchService.Search(ctx, params)
	if err != nil {
		h.logger.Warn("native search failed, proxying", slog.Any("error", err))
		h.proxyWithBody(w, r, normalized)
		return
	}

	resp := map[string]any{
		"code":    200,
		"message": "Search completed successfully",
		"data":    output.Result,
	}
	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, search.CacheTTL)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encoded)
	} else {
		h.writeJSON(w, http.StatusOK, resp)
	}

	h.logSearchResult(output.Result, params.Query, params.Dedup)
}

// logSearchResult logs result counts after a successful native search.
func (h *Handler) logSearchResult(result *search.SearchResult, query, dedup string) {
	if result == nil {
		return
	}
	textCount, skillCount, prefCount, toolCount := 0, 0, 0, 0
	if len(result.TextMem) > 0 {
		textCount = len(result.TextMem[0].Memories)
	}
	if len(result.SkillMem) > 0 {
		skillCount = len(result.SkillMem[0].Memories)
	}
	if len(result.PrefMem) > 0 {
		prefCount = len(result.PrefMem[0].Memories)
	}
	if len(result.ToolMem) > 0 {
		toolCount = len(result.ToolMem[0].Memories)
	}
	h.logger.Info("native search complete",
		slog.String("query", truncateQuery(query)),
		slog.Int("text", textCount),
		slog.Int("skill", skillCount),
		slog.Int("pref", prefCount),
		slog.Int("tool", toolCount),
		slog.String("dedup", dedup),
	)
}

// buildSearchParams extracts SearchParams from a validated searchRequest.
// Three-tier precedence: hardcoded defaults < profile overrides < per-request fields.
func buildSearchParams(req searchRequest) (search.SearchParams, error) {
	userName := *req.UserID
	cubeID := userName
	if req.ReadableCubeIDs != nil && len(*req.ReadableCubeIDs) > 0 {
		cubeID = (*req.ReadableCubeIDs)[0]
	}

	// 1. Start with hardcoded defaults.
	params := search.SearchParams{
		Query:            strings.TrimSpace(*req.Query),
		UserName:         userName,
		CubeID:           cubeID,
		AgentID:          stringOrEmpty(req.AgentID),
		TopK:             search.DefaultTextTopK,
		SkillTopK:        search.DefaultSkillTopK,
		PrefTopK:         search.DefaultPrefTopK,
		ToolTopK:         search.DefaultToolTopK,
		Dedup:            "no",
		Relativity:       search.DefaultRelativity,
		IncludeEmbedding: derefBoolOr(req.IncludeEmbedding, false),
		IncludeSkill:     true,
		IncludePref:      true,
		IncludeTool:      false,
		NumStages:        0,
	}

	// 2. Apply profile overrides (if any).
	if req.Profile != nil {
		prof, err := search.LookupProfile(*req.Profile)
		if err != nil {
			return search.SearchParams{}, err
		}
		params = search.ApplyProfile(params, prof)
	}

	// 3. Apply per-request overrides (explicit fields win).
	applySearchOverrides(&params, req)
	return params, nil
}

// applySearchOverrides applies explicit per-request fields onto params (step 3 of three-tier precedence).
// Only non-nil fields override; nil fields retain defaults or profile values.
func applySearchOverrides(params *search.SearchParams, req searchRequest) {
	if req.TopK != nil {
		params.TopK = *req.TopK
	}
	if req.SkillMemTopK != nil {
		params.SkillTopK = *req.SkillMemTopK
	}
	if req.PrefTopK != nil {
		params.PrefTopK = *req.PrefTopK
	}
	if req.ToolMemTopK != nil {
		params.ToolTopK = *req.ToolMemTopK
	}
	if req.Dedup != nil {
		params.Dedup = *req.Dedup
	}
	if req.Relativity != nil {
		params.Relativity = *req.Relativity
	}
	if req.IncludeSkillMemory != nil {
		params.IncludeSkill = *req.IncludeSkillMemory
	}
	if req.IncludePreference != nil {
		params.IncludePref = *req.IncludePreference
	}
	if req.SearchToolMemory != nil {
		params.IncludeTool = *req.SearchToolMemory
	}
	if req.NumStages != nil {
		params.NumStages = *req.NumStages
	}
	if req.LLMRerank != nil {
		params.LLMRerank = *req.LLMRerank
	}
}

// hashQuery returns first 8 chars of SHA256 hex digest.
func hashQuery(query string) string {
	h := sha256.Sum256([]byte(query))
	return hex.EncodeToString(h[:4])
}

// truncateQuery truncates a query for logging.
func truncateQuery(q string) string {
	if len(q) > logQueryTruncLen {
		return q[:logQueryTruncLen] + "..."
	}
	return q
}
