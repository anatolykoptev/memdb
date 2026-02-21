package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/MemDBai/MemDB/memdb-go/internal/search"
)

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

	// Validate required fields
	var errs []string
	if req.Query == nil || strings.TrimSpace(*req.Query) == "" {
		errs = append(errs, "query is required and must be non-empty")
	}
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.TopK != nil && *req.TopK < 1 {
		errs = append(errs, "top_k must be >= 1")
	}
	if req.Dedup != nil {
		switch *req.Dedup {
		case "no", "sim", "mmr":
		default:
			errs = append(errs, "dedup must be one of: no, sim, mmr")
		}
	}
	if req.Relativity != nil && *req.Relativity < 0 {
		errs = append(errs, "relativity must be >= 0")
	}
	if req.PrefTopK != nil && *req.PrefTopK < 0 {
		errs = append(errs, "pref_top_k must be >= 0")
	}
	if req.ToolMemTopK != nil && *req.ToolMemTopK < 0 {
		errs = append(errs, "tool_mem_top_k must be >= 0")
	}
	if req.SkillMemTopK != nil && *req.SkillMemTopK < 0 {
		errs = append(errs, "skill_mem_top_k must be >= 0")
	}

	if !h.checkErrors(w, errs) {
		return
	}

	// Proxy fallback conditions
	if h.searchService == nil || !h.searchService.CanSearch() {
		h.logger.Debug("native search: service unavailable, proxying")
		h.proxyWithBody(w, r, normalizeSearch(body))
		return
	}

	if req.Mode != nil && *req.Mode == "fine" {
		h.logger.Debug("native search: fine mode, proxying")
		h.proxyWithBody(w, r, normalizeSearch(body))
		return
	}

	if req.InternetSearch != nil && *req.InternetSearch {
		h.logger.Debug("native search: internet_search=true, proxying")
		h.proxyWithBody(w, r, normalizeSearch(body))
		return
	}

	// Extract parameters with defaults
	query := strings.TrimSpace(*req.Query)
	userName := *req.UserID

	topK := search.DefaultTextTopK
	if req.TopK != nil {
		topK = *req.TopK
	}
	skillTopK := search.DefaultSkillTopK
	if req.SkillMemTopK != nil {
		skillTopK = *req.SkillMemTopK
	}
	prefTopK := search.DefaultPrefTopK
	if req.PrefTopK != nil {
		prefTopK = *req.PrefTopK
	}
	toolTopK := search.DefaultToolTopK
	if req.ToolMemTopK != nil {
		toolTopK = *req.ToolMemTopK
	}
	dedup := "no"
	if req.Dedup != nil {
		dedup = *req.Dedup
	}
	relativity := 0.0
	if req.Relativity != nil {
		relativity = *req.Relativity
	}
	includeEmbedding := false
	if req.IncludeEmbedding != nil {
		includeEmbedding = *req.IncludeEmbedding
	}
	includeSkill := true
	if req.IncludeSkillMemory != nil {
		includeSkill = *req.IncludeSkillMemory
	}
	includePref := true
	if req.IncludePreference != nil {
		includePref = *req.IncludePreference
	}
	includeTool := false // Python default: search_tool_memory=False
	if req.SearchToolMemory != nil {
		includeTool = *req.SearchToolMemory
	}

	cubeID := userName
	if req.ReadableCubeIDs != nil && len(*req.ReadableCubeIDs) > 0 {
		cubeID = (*req.ReadableCubeIDs)[0]
	}

	ctx := r.Context()

	// Check cache
	cacheKey := fmt.Sprintf("%ssearch:%s:%s:%d:%d:%d:%s",
		cachePrefix, userName, hashQuery(query), topK, skillTopK, prefTopK, dedup)
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	// Call unified search service
	output, err := h.searchService.Search(ctx, search.SearchParams{
		Query:            query,
		UserName:         userName,
		CubeID:           cubeID,
		AgentID:          stringOrEmpty(req.AgentID),
		TopK:             topK,
		SkillTopK:        skillTopK,
		PrefTopK:         prefTopK,
		ToolTopK:         toolTopK,
		Dedup:            dedup,
		Relativity:       relativity,
		IncludeSkill:     includeSkill,
		IncludePref:      includePref,
		IncludeTool:      includeTool,
		IncludeEmbedding: includeEmbedding,
	})
	if err != nil {
		h.logger.Warn("native search failed, proxying",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, normalizeSearch(body))
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
		w.Write(encoded)
	} else {
		h.writeJSON(w, http.StatusOK, resp)
	}

	// Log result counts
	textCount, skillCount, prefCount, toolCount := 0, 0, 0, 0
	if len(output.Result.TextMem) > 0 {
		textCount = len(output.Result.TextMem[0].Memories)
	}
	if len(output.Result.SkillMem) > 0 {
		skillCount = len(output.Result.SkillMem[0].Memories)
	}
	if len(output.Result.PrefMem) > 0 {
		prefCount = len(output.Result.PrefMem[0].Memories)
	}
	if len(output.Result.ToolMem) > 0 {
		toolCount = len(output.Result.ToolMem[0].Memories)
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

// hashQuery returns first 8 chars of SHA256 hex digest.
func hashQuery(query string) string {
	h := sha256.Sum256([]byte(query))
	return fmt.Sprintf("%x", h[:4])
}

// truncateQuery truncates a query for logging.
func truncateQuery(q string) string {
	if len(q) > 60 {
		return q[:60] + "..."
	}
	return q
}
