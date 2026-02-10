package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/embedder"
	"github.com/MemDBai/MemDB/memdb-go/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// searchScopes are the memory types searched in PolarDB.
var searchScopes = []string{"LongTermMemory", "UserMemory", "SkillMemory"}

// prefCollections are the Qdrant collections for preference memory.
var prefCollections = []string{"explicit_preference", "implicit_preference"}

const defaultTopK = 6

// RegisterSearchTool registers the search_memories MCP tool.
func RegisterSearchTool(server *mcp.Server, pg *db.Postgres, qd *db.Qdrant, emb embedder.Embedder, logger *slog.Logger) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_memories",
		Description: "Perform semantic search through memories in accessible cubes. Returns text_mem, skill_mem, and pref_mem categories.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, TextResult, error) {
		if input.Query == "" {
			return nil, TextResult{}, fmt.Errorf("query is required")
		}

		userName := input.UserID
		if userName == "" {
			userName = "memos"
		}
		cubeID := userName
		if len(input.CubeIDs) > 0 {
			cubeID = input.CubeIDs[0]
		}

		topK := defaultTopK
		if input.TopK > 0 {
			topK = input.TopK
		}
		relativity := 0.90
		if input.Relativity > 0 {
			relativity = input.Relativity
		}
		dedup := "mmr"
		if input.Dedup != "" {
			dedup = input.Dedup
		}

		// Embed query
		embeddings, err := emb.Embed(ctx, []string{input.Query})
		if err != nil {
			return nil, TextResult{}, fmt.Errorf("embedding failed: %w", err)
		}
		queryVec := embeddings[0]

		// Parallel DB searches
		g, gctx := errgroup.WithContext(ctx)

		var vectorResults []db.VectorSearchResult
		var fulltextResults []db.VectorSearchResult
		var prefResults []db.QdrantSearchResult

		g.Go(func() error {
			var err error
			vectorResults, err = pg.VectorSearch(gctx, queryVec, userName, searchScopes, topK*5)
			return err
		})

		g.Go(func() error {
			tokens := search.TokenizeMixed(input.Query)
			tsquery := search.BuildTSQuery(tokens)
			if tsquery == "" {
				return nil
			}
			var err error
			fulltextResults, err = pg.FulltextSearch(gctx, tsquery, userName, searchScopes, topK*5)
			return err
		})

		if qd != nil {
			g.Go(func() error {
				for _, coll := range prefCollections {
					results, err := qd.SearchByVector(gctx, coll, queryVec, uint64(topK*5), userName)
					if err != nil {
						logger.Debug("pref search failed", slog.String("collection", coll), slog.Any("error", err))
						continue
					}
					prefResults = append(prefResults, results...)
				}
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return nil, TextResult{}, fmt.Errorf("search failed: %w", err)
		}

		// Merge vector + fulltext
		merged := mergeSearchResults(vectorResults, fulltextResults)

		// Format
		formatted := make([]map[string]any, 0, len(merged))
		for _, m := range merged {
			props := parseProps(m.Properties)
			if props == nil {
				continue
			}
			item := search.FormatMemoryItem(props, false)
			if meta, ok := item["metadata"].(map[string]any); ok {
				meta["relativity"] = m.Score
			}
			formatted = append(formatted, item)
		}

		// Filter by relativity threshold
		if relativity > 0 {
			filtered := make([]map[string]any, 0, len(formatted))
			for _, item := range formatted {
				if meta, ok := item["metadata"].(map[string]any); ok {
					if score, ok := meta["relativity"].(float64); ok && score >= relativity {
						filtered = append(filtered, item)
					}
				}
			}
			formatted = filtered
		}

		// Text-exact dedup (embedding-based dedup handled by REST handler, not MCP)
		if dedup != "no" && len(formatted) > 1 {
			seen := make(map[string]bool)
			deduped := make([]map[string]any, 0, len(formatted))
			for _, item := range formatted {
				mem, _ := item["memory"].(string)
				key := strings.TrimSpace(mem)
				if key == "" || seen[key] {
					continue
				}
				seen[key] = true
				deduped = append(deduped, item)
			}
			formatted = deduped
		}

		// Split by type
		factMem, _, skillMem := search.SplitByMemoryType(formatted)
		if len(factMem) > topK {
			factMem = factMem[:topK]
		}
		if len(skillMem) > topK {
			skillMem = skillMem[:topK]
		}

		// Format pref results + apply relativity threshold
		prefFormatted := formatPrefSearchResults(prefResults)
		if relativity > 0 {
			filtered := make([]map[string]any, 0, len(prefFormatted))
			for _, item := range prefFormatted {
				if meta, ok := item["metadata"].(map[string]any); ok {
					if score, ok := meta["relativity"].(float64); ok && score >= relativity {
						filtered = append(filtered, item)
					}
				}
			}
			prefFormatted = filtered
		}
		if len(prefFormatted) > topK {
			prefFormatted = prefFormatted[:topK]
		}

		result := search.BuildSearchResult(factMem, skillMem, prefFormatted, cubeID)
		return nil, TextResult{Result: result}, nil
	})
}

type mergedItem struct {
	ID         string
	Properties string
	Score      float64
}

func mergeSearchResults(vector, fulltext []db.VectorSearchResult) []mergedItem {
	byID := make(map[string]*mergedItem, len(vector)+len(fulltext))
	order := make([]string, 0, len(vector)+len(fulltext))

	for _, r := range vector {
		score := (r.Score + 1.0) / 2.0
		if existing, ok := byID[r.ID]; ok {
			if score > existing.Score {
				existing.Score = score
			}
		} else {
			byID[r.ID] = &mergedItem{ID: r.ID, Properties: r.Properties, Score: score}
			order = append(order, r.ID)
		}
	}

	for _, r := range fulltext {
		ftScore := r.Score * 0.5
		if existing, ok := byID[r.ID]; ok {
			existing.Score += ftScore * 0.1
		} else {
			byID[r.ID] = &mergedItem{ID: r.ID, Properties: r.Properties, Score: ftScore}
			order = append(order, r.ID)
		}
	}

	results := make([]mergedItem, 0, len(byID))
	for _, id := range order {
		results = append(results, *byID[id])
	}
	for i := range results {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
	return results
}

func formatPrefSearchResults(results []db.QdrantSearchResult) []map[string]any {
	formatted := make([]map[string]any, 0, len(results))
	seen := make(map[string]bool)

	for _, r := range results {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true

		memory, _ := r.Payload["memory"].(string)
		if memory == "" {
			memory, _ = r.Payload["memory_content"].(string)
		}
		if memory == "" {
			continue
		}

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

		formatted = append(formatted, map[string]any{
			"id":       r.ID,
			"ref_id":   refIDStr,
			"memory":   memory,
			"metadata": metadata,
		})
	}
	return formatted
}

func parseProps(propsJSON string) map[string]any {
	if propsJSON == "" {
		return nil
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
		return nil
	}
	return props
}
