package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/search"
)

// Default search parameters matching Python defaults.
const (
	defaultTopK        = 6
	defaultPrefTopK    = 6
	defaultSkillTopK   = 6
	searchCacheTTL     = 30 * time.Second
	searchInflateFactor = 5 // inflate top_k for dedup modes
)

// searchScopes are the memory types searched in PolarDB.
var searchScopes = []string{"LongTermMemory", "UserMemory", "SkillMemory"}

// prefCollections are the Qdrant collections for preference memory.
var prefCollections = []string{"explicit_preference", "implicit_preference"}

// NativeSearch handles POST /product/search with native Go implementation.
// Falls back to Python proxy when:
//   - embedder or postgres is nil
//   - mode is "fine" (needs LLM)
//   - internet_search is true (needs SearXNG)
//   - any error during native search
func (h *Handler) NativeSearch(w http.ResponseWriter, r *http.Request) {
	// Read and validate request body (reuses existing validation)
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
	if h.embedder == nil || h.postgres == nil {
		h.logger.Debug("native search: missing embedder or postgres, proxying")
		h.proxyWithBody(w, r, normalizeSearch(body))
		return
	}

	// Fine mode needs LLM — proxy
	if req.Mode != nil && *req.Mode == "fine" {
		h.logger.Debug("native search: fine mode, proxying")
		h.proxyWithBody(w, r, normalizeSearch(body))
		return
	}

	// Internet search needs SearXNG — proxy
	if req.InternetSearch != nil && *req.InternetSearch {
		h.logger.Debug("native search: internet_search=true, proxying")
		h.proxyWithBody(w, r, normalizeSearch(body))
		return
	}

	// Extract parameters with defaults
	query := strings.TrimSpace(*req.Query)
	userName := *req.UserID // user_id is used as user_name for search filters
	topK := defaultTopK
	if req.TopK != nil {
		topK = *req.TopK
	}
	prefTopK := defaultPrefTopK
	if req.PrefTopK != nil {
		prefTopK = *req.PrefTopK
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

	// Resolve cube_id for response formatting
	cubeID := userName
	if req.ReadableCubeIDs != nil && len(*req.ReadableCubeIDs) > 0 {
		cubeID = (*req.ReadableCubeIDs)[0]
	}

	// Inflate top_k for dedup modes
	searchTopK := topK
	searchPrefTopK := prefTopK
	if dedup == "sim" || dedup == "mmr" {
		searchTopK = topK * searchInflateFactor
		searchPrefTopK = prefTopK * searchInflateFactor
	}

	ctx := r.Context()

	// Check cache
	cacheKey := fmt.Sprintf("%ssearch:%s:%s:%d:%s",
		cachePrefix, userName, hashQuery(query), topK, dedup)
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(cached)
		return
	}

	// Step 1: Embed query (e5 models need "query: " prefix for retrieval)
	embeddings, err := h.embedder.Embed(ctx, []string{"query: " + query})
	if err != nil {
		h.logger.Warn("native search: embed failed, proxying",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, normalizeSearch(body))
		return
	}
	queryVec := embeddings[0]

	// Step 2: Parallel DB searches via errgroup
	g, gctx := errgroup.WithContext(ctx)

	var vectorResults []db.VectorSearchResult
	var fulltextResults []db.VectorSearchResult
	var prefResults []db.QdrantSearchResult

	// Goroutine 1: Vector search across all scopes
	g.Go(func() error {
		var err error
		vectorResults, err = h.postgres.VectorSearch(gctx, queryVec, userName, searchScopes, searchTopK)
		return err
	})

	// Goroutine 2: Fulltext search
	g.Go(func() error {
		tokens := search.TokenizeMixed(query)
		tsquery := search.BuildTSQuery(tokens)
		if tsquery == "" {
			return nil // no tokens = no fulltext results
		}
		var err error
		fulltextResults, err = h.postgres.FulltextSearch(gctx, tsquery, userName, searchScopes, searchTopK)
		return err
	})

	// Goroutine 3: Preference search (both collections sequentially)
	if h.qdrant != nil && prefTopK > 0 {
		g.Go(func() error {
			for _, coll := range prefCollections {
				results, err := h.qdrant.SearchByVector(gctx, coll, queryVec, uint64(searchPrefTopK))
				if err != nil {
					h.logger.Debug("pref search failed",
						slog.String("collection", coll),
						slog.Any("error", err),
					)
					continue // non-fatal, skip this collection
				}
				prefResults = append(prefResults, results...)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		h.logger.Warn("native search: db query failed, proxying",
			slog.Any("error", err),
		)
		h.proxyWithBody(w, r, normalizeSearch(body))
		return
	}

	// Step 3: Merge vector + fulltext results by node ID (keep highest score)
	merged := mergeResults(vectorResults, fulltextResults)

	// Step 4: Format memory items
	formatted := make([]map[string]any, 0, len(merged))
	for _, m := range merged {
		props := parseProperties(m.Properties)
		if props == nil {
			continue
		}
		item := search.FormatMemoryItem(props, includeEmbedding)
		// Inject relativity score into metadata
		if meta, ok := item["metadata"].(map[string]any); ok {
			meta["relativity"] = m.Score
		}
		formatted = append(formatted, item)
	}

	// Format preference items
	prefFormatted := formatPrefResults(prefResults)

	// Step 5: Apply relativity threshold
	if relativity > 0 {
		formatted = filterByRelativity(formatted, relativity)
		prefFormatted = filterByRelativity(prefFormatted, relativity)
	}

	// Step 6: Apply dedup
	switch dedup {
	case "sim":
		items := toSearchItems(formatted, queryVec, "text")
		items = search.DedupSim(items, topK)
		formatted = fromSearchItems(items)
		// Strip embeddings after dedup
		stripEmbeddings(formatted)
		stripEmbeddings(prefFormatted)
	case "mmr":
		allItems := toSearchItems(formatted, queryVec, "text")
		prefItems := toSearchItems(prefFormatted, queryVec, "preference")
		allItems = append(allItems, prefItems...)
		textOut, prefOut := search.DedupMMR(allItems, topK, prefTopK)
		formatted = fromSearchItems(textOut)
		prefFormatted = fromSearchItems(prefOut)
		// Strip embeddings after dedup
		stripEmbeddings(formatted)
		stripEmbeddings(prefFormatted)
	default:
		// No dedup — just trim to top_k
		if len(formatted) > topK {
			formatted = formatted[:topK]
		}
		if len(prefFormatted) > prefTopK {
			prefFormatted = prefFormatted[:prefTopK]
		}
	}

	// Step 7: Build response
	factMem, toolMem, skillMem := search.SplitByMemoryType(formatted)
	result := &search.SearchResult{
		TextMem:    []search.MemoryBucket{{CubeID: cubeID, Memories: factMem, TotalNodes: len(factMem)}},
		SkillMem:   []search.MemoryBucket{{CubeID: cubeID, Memories: skillMem, TotalNodes: len(skillMem)}},
		ToolMem:    []search.MemoryBucket{{CubeID: cubeID, Memories: toolMem, TotalNodes: len(toolMem)}},
		PrefMem:    []search.MemoryBucket{{CubeID: cubeID, Memories: prefFormatted, TotalNodes: len(prefFormatted)}},
		ActMem:     []any{},
		ParaMem:    []any{},
		PrefNote:   "",
		PrefString: "",
	}

	resp := map[string]any{
		"code":    200,
		"message": "Search completed successfully",
		"data":    result,
	}

	// Cache and respond
	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, searchCacheTTL)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(encoded)
	} else {
		h.writeJSON(w, http.StatusOK, resp)
	}

	h.logger.Info("native search complete",
		slog.String("query", truncateQuery(query)),
		slog.Int("text_results", len(factMem)),
		slog.Int("skill_results", len(skillMem)),
		slog.Int("pref_results", len(prefFormatted)),
		slog.String("dedup", dedup),
	)
}

// mergedResult combines a VectorSearchResult with a de-duped score.
type mergedResult struct {
	ID         string
	Properties string
	Score      float64
}

// mergeResults merges vector and fulltext results by node ID, keeping highest score.
func mergeResults(vector, fulltext []db.VectorSearchResult) []mergedResult {
	byID := make(map[string]*mergedResult, len(vector)+len(fulltext))
	order := make([]string, 0, len(vector)+len(fulltext))

	for _, r := range vector {
		// Normalize PolarDB cosine score: (raw_cosine + 1) / 2
		normalizedScore := (r.Score + 1.0) / 2.0
		if existing, ok := byID[r.ID]; ok {
			if normalizedScore > existing.Score {
				existing.Score = normalizedScore
			}
		} else {
			byID[r.ID] = &mergedResult{ID: r.ID, Properties: r.Properties, Score: normalizedScore}
			order = append(order, r.ID)
		}
	}

	for _, r := range fulltext {
		// Fulltext rank is already a positive score, normalize to 0-1 range
		// ts_rank returns small values (0.0-0.5 typically), boost slightly
		ftScore := r.Score * 0.5 // scale down to not overpower vector scores
		if existing, ok := byID[r.ID]; ok {
			// Boost score for items found by both vector AND fulltext
			existing.Score = existing.Score + ftScore*0.1
		} else {
			byID[r.ID] = &mergedResult{ID: r.ID, Properties: r.Properties, Score: ftScore}
			order = append(order, r.ID)
		}
	}

	// Sort by score descending
	results := make([]mergedResult, 0, len(byID))
	for _, id := range order {
		results = append(results, *byID[id])
	}
	// Simple sort by score descending
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
	return results
}

// parseProperties parses a JSON properties string into a map.
func parseProperties(propsJSON string) map[string]any {
	var props map[string]any
	if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
		return nil
	}
	return props
}

// filterByRelativity filters formatted items by their metadata.relativity score.
func filterByRelativity(items []map[string]any, threshold float64) []map[string]any {
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		meta, _ := item["metadata"].(map[string]any)
		if meta == nil {
			continue
		}
		score, _ := meta["relativity"].(float64)
		if score >= threshold {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// toSearchItems converts formatted memory items to SearchItem slice for dedup.
func toSearchItems(items []map[string]any, queryVec []float32, memType string) []search.SearchItem {
	result := make([]search.SearchItem, 0, len(items))
	for _, item := range items {
		memory, _ := item["memory"].(string)
		meta, _ := item["metadata"].(map[string]any)
		score := 0.0
		var embedding []float32
		if meta != nil {
			if s, ok := meta["relativity"].(float64); ok {
				score = s
			}
			// Try to get embedding from metadata
			if emb, ok := meta["embedding"].([]any); ok && len(emb) > 0 {
				embedding = make([]float32, len(emb))
				for i, v := range emb {
					if f, ok := v.(float64); ok {
						embedding[i] = float32(f)
					}
				}
			}
		}
		// If no embedding available, use query vector as fallback
		// (dedup will use embedding similarity matrix)
		if len(embedding) == 0 {
			embedding = queryVec
		}
		result = append(result, search.SearchItem{
			Memory:     memory,
			Score:      score,
			MemType:    memType,
			BucketIdx:  0,
			Embedding:  embedding,
			Properties: item,
		})
	}
	return result
}

// fromSearchItems converts SearchItems back to formatted memory items.
func fromSearchItems(items []search.SearchItem) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, item.Properties)
	}
	return result
}

// stripEmbeddings removes embeddings from formatted items' metadata.
func stripEmbeddings(items []map[string]any) {
	for _, item := range items {
		if meta, ok := item["metadata"].(map[string]any); ok {
			meta["embedding"] = []any{}
		}
	}
}

// formatPrefResults converts Qdrant preference results to formatted memory items.
func formatPrefResults(results []db.QdrantSearchResult) []map[string]any {
	formatted := make([]map[string]any, 0, len(results))
	seen := make(map[string]bool)

	for _, r := range results {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true

		memory, _ := r.Payload["memory"].(string)
		if memory == "" {
			// Try memory_content field
			memory, _ = r.Payload["memory_content"].(string)
		}
		if memory == "" {
			continue
		}

		// Build metadata from payload
		metadata := make(map[string]any)
		for k, v := range r.Payload {
			metadata[k] = v
		}
		metadata["relativity"] = float64(r.Score)
		metadata["embedding"] = []any{}
		metadata["usage"] = []any{}
		metadata["id"] = r.ID
		metadata["memory"] = memory

		refID := r.ID
		if idx := strings.IndexByte(refID, '-'); idx > 0 {
			refID = refID[:idx]
		}
		refIDStr := "[" + refID + "]"
		metadata["ref_id"] = refIDStr

		item := map[string]any{
			"id":       r.ID,
			"ref_id":   refIDStr,
			"memory":   memory,
			"metadata": metadata,
		}
		formatted = append(formatted, item)
	}
	return formatted
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
