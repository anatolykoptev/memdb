// Package search — unified SearchService used by both REST and MCP handlers.
package search

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/embedder"
	"github.com/MemDBai/MemDB/memdb-go/internal/scheduler"
)

// SearchService performs the full search pipeline: embed → parallel DB queries →
// merge → format → rerank → filter → dedup → trim → build response.
type SearchService struct {
	postgres    *db.Postgres
	qdrant      *db.Qdrant
	embedder    embedder.Embedder
	logger      *slog.Logger
	LLMReranker LLMRerankConfig
	Iterative   IterativeConfig
	// Profiler generates and serves Memobase-style user profile summaries.
	// When non-nil, profile_mem is populated in every search response.
	Profiler    *scheduler.Profiler
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
	AgentID          string
	TopK             int     // text budget (default DefaultTextTopK)
	SkillTopK        int     // skill budget (default DefaultSkillTopK)
	PrefTopK         int     // pref budget (default DefaultPrefTopK)
	ToolTopK         int     // tool budget (default DefaultToolTopK)
	Dedup            string  // "no", "sim", "mmr"
	MMRLambda        float64 // MMR relevance weight 0..1 (0 = use DefaultMMRLambda=0.7)
	DecayAlpha       float64 // temporal decay alpha (0 = use DefaultDecayAlpha; -1 = disabled)
	Relativity       float64 // threshold (0 = disabled)
	IncludeSkill     bool
	IncludePref      bool
	IncludeTool      bool
	IncludeEmbedding bool
	NumStages        int     // iterative expansion stages (0 = disabled, 2 = fast, 3 = fine)
}

// SearchOutput holds the formatted result plus optional embedding sidecar.
type SearchOutput struct {
	Result *SearchResult
}

// Search executes the full native search pipeline.
func (s *SearchService) Search(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	// Step 1: Embed query (uses EmbedQuery for query-specific prefix if configured)
	queryVec, err := s.embedder.EmbedQuery(ctx, p.Query)
	if err != nil {
		return nil, err
	}

	// Step 2: Tokenize for fulltext + temporal detection
	tokens := TokenizeMixed(p.Query)
	tsquery := BuildTSQuery(tokens)
	temporalCutoff := DetectTemporalCutoff(p.Query)
	hasCutoff := !temporalCutoff.IsZero()
	var cutoffISO string
	if hasCutoff {
		cutoffISO = temporalCutoff.Format("2006-01-02T15:04:05+00:00")
		s.logger.Debug("temporal scope detected",
			slog.String("cutoff", cutoffISO),
			slog.String("query", p.Query),
		)
	}

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
	var graphKeyResults, graphTagResults, entityGraphResults []db.GraphRecallResult
	var workingMemItems []db.VectorSearchResult

	// Text: vector search (LTM + User) — with temporal cutoff if detected
	g.Go(func() error {
		var err error
		if hasCutoff {
			textVec, err = s.postgres.VectorSearchWithCutoff(gctx, queryVec, p.UserName, TextScopes, textK, cutoffISO, p.AgentID)
		} else {
			textVec, err = s.postgres.VectorSearch(gctx, queryVec, p.UserName, TextScopes, p.AgentID, textK)
		}
		return err
	})

	// Text: fulltext search — with temporal cutoff if detected
	if tsquery != "" {
		g.Go(func() error {
			var err error
			if hasCutoff {
				textFT, err = s.postgres.FulltextSearchWithCutoff(gctx, tsquery, p.UserName, TextScopes, textK, cutoffISO, p.AgentID)
			} else {
				textFT, err = s.postgres.FulltextSearch(gctx, tsquery, p.UserName, TextScopes, p.AgentID, textK)
			}
			return err
		})
	}

	// Skill: vector + fulltext (no temporal cutoff — skills are timeless)
	if p.IncludeSkill && p.SkillTopK > 0 {
		g.Go(func() error {
			var err error
			skillVec, err = s.postgres.VectorSearch(gctx, queryVec, p.UserName, SkillScopes, p.AgentID, skillK)
			return err
		})
		if tsquery != "" {
			g.Go(func() error {
				var err error
				skillFT, err = s.postgres.FulltextSearch(gctx, tsquery, p.UserName, SkillScopes, p.AgentID, skillK)
				return err
			})
		}
	}

	// Tool: vector + fulltext (no temporal cutoff)
	if p.IncludeTool && p.ToolTopK > 0 {
		g.Go(func() error {
			var err error
			toolVec, err = s.postgres.VectorSearch(gctx, queryVec, p.UserName, ToolScopes, p.AgentID, toolK)
			return err
		})
		if tsquery != "" {
			g.Go(func() error {
				var err error
				toolFT, err = s.postgres.FulltextSearch(gctx, tsquery, p.UserName, ToolScopes, p.AgentID, toolK)
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

	// WorkingMemory: fetch recent session context, score by cosine in Go
	g.Go(func() error {
		var err error
		workingMemItems, err = s.postgres.GetWorkingMemory(gctx, p.UserName, WorkingMemoryLimit, p.AgentID)
		if err != nil {
			s.logger.Debug("working memory fetch failed", slog.Any("error", err))
			return nil // non-fatal
		}
		return nil
	})

	// Graph recall by key (tokens become candidate keys)
	if len(tokens) > 0 {
		g.Go(func() error {
			var err error
			graphKeyResults, err = s.postgres.GraphRecallByKey(gctx, p.UserName, GraphRecallScopes, tokens, p.AgentID, GraphRecallLimit)
			if err != nil {
				s.logger.Debug("graph recall by key failed", slog.Any("error", err))
				return nil // non-fatal
			}
			return nil
		})
	}

	// Graph recall by tags (tokens become candidate tags)
	if len(tokens) >= 2 {
		g.Go(func() error {
			var err error
			graphTagResults, err = s.postgres.GraphRecallByTags(gctx, p.UserName, GraphRecallScopes, tokens, p.AgentID, GraphRecallLimit)
			if err != nil {
				s.logger.Debug("graph recall by tags failed", slog.Any("error", err))
				return nil // non-fatal
			}
			return nil
		})
	}

	// Entity graph recall: normalize query tokens → find matching entity_nodes → get their memories.
	// Runs in parallel with other graph recalls. Non-fatal.
	if len(tokens) > 0 {
		g.Go(func() error {
			normalized := make([]string, len(tokens))
			for i, t := range tokens {
				normalized[i] = db.NormalizeEntityID(t)
			}
			entityIDs, err := s.postgres.FindEntitiesByNormalizedID(gctx, normalized, p.UserName)
			if err != nil || len(entityIDs) == 0 {
				return nil
			}
			entityGraphResults, err = s.postgres.GetMemoriesByEntityIDs(gctx, entityIDs, p.UserName, GraphRecallLimit)
			if err != nil {
				s.logger.Debug("entity graph recall failed", slog.Any("error", err))
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// BFS multi-hop expansion: take top-5 vector hits as seeds and traverse 2 hops.
	// Runs serially after the parallel phase (seeds depend on textVec result).
	// Non-fatal: BFS errors are logged, pipeline continues with empty expansion.
	var bfsResults []db.GraphRecallResult
	if len(textVec) > 0 {
		seedN := 5
		if len(textVec) < seedN {
			seedN = len(textVec)
		}
		seedIDs := make([]string, 0, seedN)
		for _, r := range textVec[:seedN] {
			seedIDs = append(seedIDs, r.ID)
		}
		bfs, err := s.postgres.GraphBFSTraversal(gctx, seedIDs, p.UserName, GraphRecallScopes, 2, GraphRecallLimit, p.AgentID)
		if err != nil {
			s.logger.Debug("graph bfs traversal failed", slog.Any("error", err))
		} else {
			bfsResults = bfs
		}
	}

	// Step 4: Merge per type
	textMerged := MergeVectorAndFulltext(textVec, textFT)
	skillMerged := MergeVectorAndFulltext(skillVec, skillFT)
	toolMerged := MergeVectorAndFulltext(toolVec, toolFT)

	// Merge graph recall results (key + tag + multi-hop BFS + entity graph) into text and skill buckets
	graphAll := append(graphKeyResults, graphTagResults...)
	graphAll = append(graphAll, bfsResults...)
	graphAll = append(graphAll, entityGraphResults...)
	if len(graphAll) > 0 {
		// Split graph results by scope into text vs skill
		var graphText, graphSkill []db.GraphRecallResult
		for _, g := range graphAll {
			props := ParseProperties(g.Properties)
			if props == nil {
				continue
			}
			mtype, _ := props["memory_type"].(string)
			switch mtype {
			case "SkillMemory":
				graphSkill = append(graphSkill, g)
			default:
				graphText = append(graphText, g)
			}
		}
		textMerged = MergeGraphIntoResults(textMerged, graphText)
		if p.IncludeSkill && p.SkillTopK > 0 {
			skillMerged = MergeGraphIntoResults(skillMerged, graphSkill)
		}
	}

	// Step 4.5: CONTRADICTS penalty — lower score of memories that are contradicted
	// by higher-ranked results. Uses CONTRADICTS edges written by the reorganizer.
	// Non-fatal: skip on error, pipeline continues with unpenalized results.
	if len(textMerged) > 0 {
		seedN := 10
		if len(textMerged) < seedN {
			seedN = len(textMerged)
		}
		seedIDs := make([]string, 0, seedN)
		for _, r := range textMerged[:seedN] {
			seedIDs = append(seedIDs, r.ID)
		}
		contradicted, err := s.postgres.GraphRecallByEdge(gctx, seedIDs, db.EdgeContradicts, p.UserName, 20)
		if err == nil && len(contradicted) > 0 {
			contradictedSet := make(map[string]bool, len(contradicted))
			for _, c := range contradicted {
				contradictedSet[c.ID] = true
			}
			textMerged = PenalizeContradicts(textMerged, contradictedSet)
		}
	}

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

	// Step 6.1: LLM rerank of text_mem (optional, cached, non-fatal)
	// Applied on top of cosine scores for better semantic relevance ordering.
	if s.LLMReranker.APIURL != "" && len(textFormatted) > 1 {
		textFormatted = LLMRerank(ctx, p.Query, textFormatted, s.LLMReranker)
	}

	// Step 6.2: Iterative multi-stage retrieval expansion (MemOS AdvancedSearcher port).
	// After first-pass recall + rerank, ask LLM if current memories cover the query.
	// If not, generate sub-queries and expand with additional vector searches.
	// Non-fatal: failures return unmodified textFormatted.
	numStages := p.NumStages
	if numStages == 0 && s.Iterative.NumStages > 0 {
		numStages = s.Iterative.NumStages
	}
	if numStages > 0 && s.Iterative.APIURL != "" && s.embedder != nil && s.postgres != nil {
		// embedFn: given a sub-query, embed it and run a fresh vector search.
		embedFn := func(subCtx context.Context, subQuery string) ([]map[string]any, error) {
			vecs, err := s.embedder.Embed(subCtx, []string{subQuery})
			if err != nil || len(vecs) == 0 {
				return nil, err
			}
			subVec := vecs[0]
			results, err := s.postgres.VectorSearch(subCtx, subVec, p.UserName, TextScopes, p.AgentID, p.TopK*InflateFactor)
			if err != nil {
				return nil, err
			}
			merged := MergeVectorAndFulltext(results, nil)
			formatted, embByID := FormatMergedItems(merged, true)
			formatted = ReRankByCosine(subVec, formatted, embByID)
			return formatted, nil
		}
		extCfg := s.Iterative
		extCfg.NumStages = numStages
		textFormatted = IterativeExpand(ctx, p.Query, textFormatted, embedFn, extCfg)
		s.logger.Debug("iterative expansion complete",
			slog.String("query", p.Query),
			slog.Int("total_items", len(textFormatted)),
		)
	}

	// Step 6.5: Temporal decay — score *= exp(-alpha * days_since_created)
	// Applied after cosine rerank so decay modulates the cosine score, not raw DB scores.
	// WorkingMemory is exempt (session context, recency is its purpose).
	decayAlpha := p.DecayAlpha
	if decayAlpha == 0 {
		decayAlpha = DefaultDecayAlpha
	}
	if decayAlpha > 0 {
		now := time.Now()
		textFormatted = ApplyTemporalDecay(textFormatted, now, decayAlpha)
		skillFormatted = ApplyTemporalDecay(skillFormatted, now, decayAlpha)
		toolFormatted = ApplyTemporalDecay(toolFormatted, now, decayAlpha)
	}

	// Step 7: Relativity threshold — filter all memory types for consistent quality
	if p.Relativity > 0 {
		textFormatted = FilterByRelativity(textFormatted, p.Relativity)
		skillFormatted = FilterByRelativity(skillFormatted, p.Relativity)
		toolFormatted = FilterByRelativity(toolFormatted, p.Relativity)
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
			mmrLambda := p.MMRLambda
			if mmrLambda <= 0 || mmrLambda > 1 {
				mmrLambda = DefaultMMRLambda
			}
			dedupedText, dedupedPref := DedupMMR(combined, p.TopK, p.PrefTopK, queryVec, mmrLambda)
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

	// Step 10: Cross-source dedup — remove items from skill/tool/pref that
	// duplicate text already in text_mem (text has highest priority).
	skillFormatted, toolFormatted, prefFormatted = CrossSourceDedupByText(textFormatted, skillFormatted, toolFormatted, prefFormatted)

	// Step 11: Trim each type to its budget
	textFormatted = TrimSlice(textFormatted, p.TopK)
	skillFormatted = TrimSlice(skillFormatted, p.SkillTopK)
	toolFormatted = TrimSlice(toolFormatted, p.ToolTopK)
	prefFormatted = TrimSlice(prefFormatted, p.PrefTopK)

	// Strip embeddings from response
	StripEmbeddings(textFormatted)
	StripEmbeddings(skillFormatted)
	StripEmbeddings(toolFormatted)
	StripEmbeddings(prefFormatted)

	// Step 12: WorkingMemory → ActMem: score by cosine, soft relativity threshold
	var actMemFormatted []map[string]any
	if len(workingMemItems) > 0 {
		wmMerged := make([]MergedResult, 0, len(workingMemItems))
		for _, r := range workingMemItems {
			wmMerged = append(wmMerged, MergedResult{
				ID:         r.ID,
				Properties: r.Properties,
				Score:      WorkingMemBaseScore,
				Embedding:  r.Embedding,
			})
		}
		actFormatted, actEmbByID := FormatMergedItems(wmMerged, false)
		actFormatted = ReRankByCosine(queryVec, actFormatted, actEmbByID)
		if p.Relativity > 0 {
			threshold := p.Relativity - 0.10 // softer threshold — WM is session context
			if threshold > 0 {
				actFormatted = FilterByRelativity(actFormatted, threshold)
			}
		}
		actFormatted = TrimSlice(actFormatted, WorkingMemoryLimit)
		StripEmbeddings(actFormatted)
		actMemFormatted = actFormatted
	}

	// Step 12: Build response
	result := buildFullSearchResult(textFormatted, skillFormatted, toolFormatted, prefFormatted, actMemFormatted, p.CubeID)

	// Step 12.5: Inject Memobase-style user profile summary.
	// Reads from Redis (in-process cache hit, ~0ms). Non-fatal if unavailable.
	if s.Profiler != nil && p.CubeID != "" {
		result.ProfileMem = s.Profiler.GetProfile(ctx, p.CubeID)
	}

	// Step 13: Async retrieval_count increment + importance_score boost.
	// Fire-and-forget: does not block the response. Collects IDs from all LTM/UserMemory results.
	if s.postgres != nil {
		if ids := collectResultIDs(textFormatted, skillFormatted); len(ids) > 0 {
			nowStr := time.Now().UTC().Format("2006-01-02T15:04:05.000000")
			go func() {
				if err := s.postgres.IncrRetrievalCount(context.Background(), ids, nowStr); err != nil {
					s.logger.Debug("incr retrieval count failed", slog.Any("error", err))
				}
			}()
		}
	}

	return &SearchOutput{Result: result}, nil
}

// collectResultIDs extracts the database node IDs from formatted search result slices.
// Used to batch-increment retrieval_count after a search response is built.
// Reads the "id" field from each item's metadata (set by FormatMemoryItem).
func collectResultIDs(buckets ...[]map[string]any) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, bucket := range buckets {
		for _, item := range bucket {
			meta, _ := item["metadata"].(map[string]any)
			if meta == nil {
				continue
			}
			id, _ := meta["id"].(string)
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

// buildFullSearchResult creates a SearchResult with all memory types.
func buildFullSearchResult(text, skill, tool, pref, actMem []map[string]any, cubeID string) *SearchResult {
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

	// Convert actMem to []any for JSON compatibility.
	actAny := make([]any, 0, len(actMem))
	for _, item := range actMem {
		actAny = append(actAny, item)
	}

	return &SearchResult{
		TextMem:  []MemoryBucket{{CubeID: cubeID, Memories: text, TotalNodes: len(text)}},
		SkillMem: []MemoryBucket{{CubeID: cubeID, Memories: skill, TotalNodes: len(skill)}},
		ToolMem:  []MemoryBucket{{CubeID: cubeID, Memories: tool, TotalNodes: len(tool)}},
		PrefMem:  []MemoryBucket{{CubeID: cubeID, Memories: pref, TotalNodes: len(pref)}},
		ActMem:   actAny,
		ParaMem:  []any{},
	}
}
