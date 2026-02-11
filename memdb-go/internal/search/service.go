// Package search — unified SearchService used by both REST and MCP handlers.
package search

import (
	"context"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/embedder"
)

// SearchService performs the full search pipeline: embed → parallel DB queries →
// merge → format → rerank → filter → dedup → trim → build response.
type SearchService struct {
	postgres *db.Postgres
	qdrant   *db.Qdrant
	embedder embedder.Embedder
	logger   *slog.Logger
}

// NewSearchService creates a SearchService. Any dependency may be nil (caller
// should check CanSearch before calling Search).
func NewSearchService(pg *db.Postgres, qd *db.Qdrant, emb embedder.Embedder, logger *slog.Logger) *SearchService {
	return &SearchService{
		postgres: pg,
		qdrant:   qd,
		embedder: emb,
		logger:   logger,
	}
}

// CanSearch returns true if the minimum dependencies (embedder + postgres) are available.
func (s *SearchService) CanSearch() bool {
	return s.embedder != nil && s.postgres != nil
}

// SearchParams configures a single search invocation.
type SearchParams struct {
	Query            string
	UserName         string
	CubeID           string
	TopK             int     // text budget (default DefaultTextTopK)
	SkillTopK        int     // skill budget (default DefaultSkillTopK)
	PrefTopK         int     // pref budget (default DefaultPrefTopK)
	ToolTopK         int     // tool budget (default DefaultToolTopK)
	Dedup            string  // "no", "sim", "mmr"
	Relativity       float64 // threshold (0 = disabled)
	IncludeSkill     bool
	IncludePref      bool
	IncludeTool      bool
	IncludeEmbedding bool
}

// SearchOutput holds the formatted result plus optional embedding sidecar.
type SearchOutput struct {
	Result *SearchResult
}

// Search executes the full native search pipeline.
func (s *SearchService) Search(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	// Step 1: Embed query
	embeddings, err := s.embedder.Embed(ctx, []string{p.Query})
	if err != nil {
		return nil, err
	}
	queryVec := embeddings[0]

	// Step 2: Tokenize for fulltext
	tokens := TokenizeMixed(p.Query)
	tsquery := BuildTSQuery(tokens)

	// Inflate top_k for dedup modes
	textK := p.TopK
	skillK := p.SkillTopK
	prefK := p.PrefTopK
	toolK := p.ToolTopK
	if p.Dedup == "sim" || p.Dedup == "mmr" {
		textK = p.TopK * InflateFactor
		skillK = p.SkillTopK * InflateFactor
		prefK = p.PrefTopK * InflateFactor
		toolK = p.ToolTopK * InflateFactor
	}

	// Step 3: Parallel DB searches via errgroup
	g, gctx := errgroup.WithContext(ctx)

	var textVec, textFT []db.VectorSearchResult
	var skillVec, skillFT []db.VectorSearchResult
	var toolVec, toolFT []db.VectorSearchResult
	var prefResults []db.QdrantSearchResult

	// Text: vector search (LTM + User)
	g.Go(func() error {
		var err error
		textVec, err = s.postgres.VectorSearch(gctx, queryVec, p.UserName, TextScopes, textK)
		return err
	})

	// Text: fulltext search
	if tsquery != "" {
		g.Go(func() error {
			var err error
			textFT, err = s.postgres.FulltextSearch(gctx, tsquery, p.UserName, TextScopes, textK)
			return err
		})
	}

	// Skill: vector + fulltext
	if p.IncludeSkill && p.SkillTopK > 0 {
		g.Go(func() error {
			var err error
			skillVec, err = s.postgres.VectorSearch(gctx, queryVec, p.UserName, SkillScopes, skillK)
			return err
		})
		if tsquery != "" {
			g.Go(func() error {
				var err error
				skillFT, err = s.postgres.FulltextSearch(gctx, tsquery, p.UserName, SkillScopes, skillK)
				return err
			})
		}
	}

	// Tool: vector + fulltext
	if p.IncludeTool && p.ToolTopK > 0 {
		g.Go(func() error {
			var err error
			toolVec, err = s.postgres.VectorSearch(gctx, queryVec, p.UserName, ToolScopes, toolK)
			return err
		})
		if tsquery != "" {
			g.Go(func() error {
				var err error
				toolFT, err = s.postgres.FulltextSearch(gctx, tsquery, p.UserName, ToolScopes, toolK)
				return err
			})
		}
	}

	// Preference: Qdrant search
	if s.qdrant != nil && p.IncludePref && p.PrefTopK > 0 {
		g.Go(func() error {
			for _, coll := range PrefCollections {
				results, err := s.qdrant.SearchByVector(gctx, coll, queryVec, uint64(prefK), p.UserName)
				if err != nil {
					s.logger.Debug("pref search failed",
						slog.String("collection", coll),
						slog.Any("error", err),
					)
					continue
				}
				prefResults = append(prefResults, results...)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Step 4: Merge per type
	textMerged := MergeVectorAndFulltext(textVec, textFT)
	skillMerged := MergeVectorAndFulltext(skillVec, skillFT)
	toolMerged := MergeVectorAndFulltext(toolVec, toolFT)

	// Step 5: Format per type — FormatMergedItems always builds the
	// embeddingByID sidecar regardless of IncludeEmbedding (which only
	// controls whether embedding appears in the JSON metadata output).
	textFormatted, textEmbByID := FormatMergedItems(textMerged, p.IncludeEmbedding)
	skillFormatted, skillEmbByID := FormatMergedItems(skillMerged, p.IncludeEmbedding)
	toolFormatted, toolEmbByID := FormatMergedItems(toolMerged, p.IncludeEmbedding)
	prefFormatted := FormatPrefResults(prefResults)

	// Step 6: Cosine rerank — replaces compressed PolarDB scores with direct cosine similarity
	textFormatted = ReRankByCosine(queryVec, textFormatted, textEmbByID)
	skillFormatted = ReRankByCosine(queryVec, skillFormatted, skillEmbByID)
	toolFormatted = ReRankByCosine(queryVec, toolFormatted, toolEmbByID)

	// Step 7: Relativity threshold — Python only filters text_mem and pref_mem
	// (skill_mem and tool_mem are NOT filtered by relativity in Python's search_handler)
	if p.Relativity > 0 {
		textFormatted = FilterByRelativity(textFormatted, p.Relativity)
		// Pref scores are naturally lower; apply a softer threshold.
		prefThreshold := p.Relativity - 0.10
		if prefThreshold > 0 {
			prefFormatted = FilterByRelativity(prefFormatted, prefThreshold)
		}
	}

	// Step 8: Pref quality filter
	prefFormatted = FilterPrefByQuality(prefFormatted)

	// Step 9: Dedup per type
	switch p.Dedup {
	case "sim":
		textItems := ToSearchItems(textFormatted, textEmbByID, "text")
		textItems = DedupSim(textItems, p.TopK)
		textFormatted = FromSearchItems(textItems)
		skillFormatted = DedupByText(skillFormatted)
		toolFormatted = DedupByText(toolFormatted)
		prefFormatted = DedupByText(prefFormatted)
	case "mmr":
		// Build combined text+pref items for proper MMR dedup
		textItems := ToSearchItems(textFormatted, textEmbByID, "text")
		prefItems := ToSearchItems(prefFormatted, nil, "preference")
		combined := append(textItems, prefItems...)
		if len(combined) > 0 {
			dedupedText, dedupedPref := DedupMMR(combined, p.TopK, p.PrefTopK)
			textFormatted = FromSearchItems(dedupedText)
			prefFormatted = FromSearchItems(dedupedPref)
		}
		skillFormatted = DedupByText(skillFormatted)
		toolFormatted = DedupByText(toolFormatted)
	default:
		// No dedup — Python does nothing here. We keep exact-text dedup as a
		// Go-specific safety net (prevents identical memories from appearing
		// twice due to vector+fulltext merge). Cost is negligible.
		textFormatted = DedupByText(textFormatted)
		skillFormatted = DedupByText(skillFormatted)
		toolFormatted = DedupByText(toolFormatted)
		prefFormatted = DedupByText(prefFormatted)
	}

	// Step 10: Trim each type to its budget
	textFormatted = TrimSlice(textFormatted, p.TopK)
	skillFormatted = TrimSlice(skillFormatted, p.SkillTopK)
	toolFormatted = TrimSlice(toolFormatted, p.ToolTopK)
	prefFormatted = TrimSlice(prefFormatted, p.PrefTopK)

	// Strip embeddings from response
	StripEmbeddings(textFormatted)
	StripEmbeddings(skillFormatted)
	StripEmbeddings(toolFormatted)
	StripEmbeddings(prefFormatted)

	// Step 11: Build response
	result := buildFullSearchResult(textFormatted, skillFormatted, toolFormatted, prefFormatted, p.CubeID)

	return &SearchOutput{Result: result}, nil
}

// buildFullSearchResult creates a SearchResult with all memory types.
func buildFullSearchResult(text, skill, tool, pref []map[string]any, cubeID string) *SearchResult {
	if text == nil {
		text = []map[string]any{}
	}
	if skill == nil {
		skill = []map[string]any{}
	}
	if tool == nil {
		tool = []map[string]any{}
	}
	if pref == nil {
		pref = []map[string]any{}
	}

	return &SearchResult{
		TextMem:  []MemoryBucket{{CubeID: cubeID, Memories: text, TotalNodes: len(text)}},
		SkillMem: []MemoryBucket{{CubeID: cubeID, Memories: skill, TotalNodes: len(skill)}},
		ToolMem:  []MemoryBucket{{CubeID: cubeID, Memories: tool, TotalNodes: len(tool)}},
		PrefMem:  []MemoryBucket{{CubeID: cubeID, Memories: pref, TotalNodes: len(pref)}},
		ActMem:   []any{},
		ParaMem:  []any{},
	}
}
