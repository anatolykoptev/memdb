// Package search — unified SearchService used by both REST and MCP handlers.
package search

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
)

// contradictsEdgeSeedN is the number of top results used as seed IDs
// for the CONTRADICTS edge recall step.
const contradictsEdgeSeedN = 20

// Dedup mode values for SearchParams.Dedup.
const (
	DedupModeSim = "sim" // similarity-based deduplication
	DedupModeMMR = "mmr" // maximal marginal relevance deduplication
	DedupModeNo  = "no"  // no deduplication
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
	Enhance     EnhanceConfig
	// Internet performs web search via SearXNG. nil = disabled.
	Internet *InternetSearcher
	// Fine configures LLM fine-mode (filter + recall). Zero value = disabled.
	Fine FineConfig
	// Profiler generates and serves Memobase-style user profile summaries.
	// When non-nil, profile_mem is populated in every search response.
	Profiler *scheduler.Profiler
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
	Query    string
	UserName string
	CubeID   string
	// CubeIDs enables cross-domain (multi-cube) vector search. When len>0, the
	// vector search filter switches from user_name = CubeID to user_name = ANY(CubeIDs).
	// Leave empty for single-cube search (default). CubeID is kept as a fallback
	// for code paths (response building, profiler, etc.) that still use one cube.
	CubeIDs          []string
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
	NumStages        int  // iterative expansion stages (0 = disabled, 2 = fast, 3 = fine)
	LLMRerank        bool // enable LLM-based reranking (adds ~3-4s latency)
	InternetSearch   bool // enable web search via SearXNG
}

// SearchOutput holds the formatted result plus optional embedding sidecar.
type SearchOutput struct {
	Result *SearchResult
}

// parallelSearchResults holds all results from the parallel DB phase.
type parallelSearchResults struct {
	textVec            []db.VectorSearchResult
	textFT             []db.VectorSearchResult
	skillVec           []db.VectorSearchResult
	skillFT            []db.VectorSearchResult
	toolVec            []db.VectorSearchResult
	toolFT             []db.VectorSearchResult
	prefResults        []db.QdrantSearchResult
	graphKeyResults    []db.GraphRecallResult
	graphTagResults    []db.GraphRecallResult
	entityGraphResults []db.GraphRecallResult
	workingMemItems    []db.VectorSearchResult
	internetResults    []InternetResult
}

// searchBudget holds the inflated top-k values for dedup modes.
type searchBudget struct {
	textK  int
	skillK int
	prefK  int
	toolK  int
}

// Search executes the full native search pipeline.
func (s *SearchService) Search(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	pipelineStart := time.Now()

	// Step 1: Embed query (uses EmbedQuery for query-specific prefix if configured)
	t0 := time.Now()
	queryVec, err := s.embedder.EmbedQuery(ctx, p.Query)
	if err != nil {
		return nil, err
	}
	embedDur := time.Since(t0)

	// Step 2: Tokenize for fulltext + temporal detection
	tokens := TokenizeMixed(p.Query)
	tsquery := BuildTSQuery(tokens)
	cutoffISO, hasCutoff := s.detectTemporalCutoff(p.Query)

	budget := s.computeBudget(p)

	// Step 3: Parallel DB searches
	t0 = time.Now()
	psr, err := s.runParallelSearches(ctx, queryVec, tokens, tsquery, cutoffISO, hasCutoff, p, budget)
	if err != nil {
		return nil, err
	}
	parallelDur := time.Since(t0)

	// BFS multi-hop expansion (serially after parallel phase)
	t0 = time.Now()
	bfsResults := s.runBFSExpansion(ctx, psr.textVec, p)
	bfsDur := time.Since(t0)

	// Step 3.5: Embed internet results (if any)
	internetMerged := s.embedInternetResults(ctx, psr.internetResults)

	// Step 4: Merge per type
	textMerged, skillMerged, toolMerged := s.mergeSearchResults(psr, bfsResults, internetMerged, p)

	// Step 4.5: CONTRADICTS penalty
	t0 = time.Now()
	textMerged = s.applyContradictsPenalty(ctx, textMerged, p)
	contradictsDur := time.Since(t0)

	// Step 5: Format per type
	textFormatted, textEmbByID := FormatMergedItems(textMerged, p.IncludeEmbedding)
	skillFormatted, skillEmbByID := FormatMergedItems(skillMerged, p.IncludeEmbedding)
	toolFormatted, toolEmbByID := FormatMergedItems(toolMerged, p.IncludeEmbedding)
	prefFormatted := FormatPrefResults(psr.prefResults)

	// Steps 6–11: Rerank, dedup, trim.
	var llmRerankDur, iterativeDur time.Duration
	textFormatted, skillFormatted, toolFormatted, prefFormatted, llmRerankDur, iterativeDur =
		s.postProcessResults(ctx, queryVec, textEmbByID, skillEmbByID, toolEmbByID, textFormatted, skillFormatted, toolFormatted, prefFormatted, p)

	// Step 12: WorkingMemory → ActMem
	actMemFormatted := s.formatWorkingMem(queryVec, psr.workingMemItems, p)

	// Step 12: Build response
	result := buildFullSearchResult(textFormatted, skillFormatted, toolFormatted, prefFormatted, actMemFormatted, p.CubeID)

	// Step 12.5: Inject user profile summary
	t0 = time.Now()
	if s.Profiler != nil && p.CubeID != "" {
		result.ProfileMem = s.Profiler.GetProfile(ctx, p.CubeID)
	}
	profileDur := time.Since(t0)

	// Pipeline timing log
	s.logger.Info("search pipeline timing",
		slog.Duration("total", time.Since(pipelineStart)),
		slog.Duration("embed", embedDur),
		slog.Duration("parallel_db", parallelDur),
		slog.Duration("bfs", bfsDur),
		slog.Duration("contradicts", contradictsDur),
		slog.Duration("llm_rerank", llmRerankDur),
		slog.Duration("iterative", iterativeDur),
		slog.Duration("profile", profileDur),
	)

	// Step 13: Async retrieval_count increment
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

// detectTemporalCutoff detects and returns the temporal cutoff ISO string and a boolean.
func (s *SearchService) detectTemporalCutoff(query string) (string, bool) {
	cutoff := DetectTemporalCutoff(query)
	if cutoff.IsZero() {
		return "", false
	}
	iso := cutoff.Format("2006-01-02T15:04:05+00:00")
	s.logger.Debug("temporal scope detected", slog.String("cutoff", iso), slog.String("query", query))
	return iso, true
}

// computeBudget computes inflated top-k budgets for dedup modes.
func (s *SearchService) computeBudget(p SearchParams) searchBudget {
	budget := searchBudget{
		textK:  p.TopK,
		skillK: p.SkillTopK,
		prefK:  p.PrefTopK,
		toolK:  p.ToolTopK,
	}
	if p.Dedup == DedupModeSim || p.Dedup == DedupModeMMR {
		budget.textK = p.TopK * InflateFactor
		budget.skillK = p.SkillTopK * InflateFactor
		budget.prefK = p.PrefTopK * InflateFactor
		budget.toolK = p.ToolTopK * InflateFactor
	}
	return budget
}

// runParallelSearches executes all DB searches concurrently.
func (s *SearchService) runParallelSearches(
	ctx context.Context,
	queryVec []float32,
	tokens []string,
	tsquery, cutoffISO string,
	hasCutoff bool,
	p SearchParams,
	budget searchBudget,
) (*parallelSearchResults, error) {
	g, gctx := errgroup.WithContext(ctx)
	psr := &parallelSearchResults{}

	s.spawnTextSearches(g, gctx, psr, queryVec, tsquery, cutoffISO, hasCutoff, p, budget)
	s.spawnSkillToolSearches(g, gctx, psr, queryVec, tsquery, p, budget)
	s.spawnPrefSearch(g, gctx, psr, queryVec, p, budget)
	s.spawnWorkingMemAndGraph(g, gctx, psr, tokens, p)
	s.spawnInternetSearch(g, gctx, psr, p)

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return psr, nil
}

// spawnTextSearches enqueues vector and fulltext search goroutines for the text scope.
func (s *SearchService) spawnTextSearches(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults,
	queryVec []float32, tsquery, cutoffISO string, hasCutoff bool,
	p SearchParams, budget searchBudget,
) {
	g.Go(func() error {
		var err error
		switch {
		case hasCutoff:
			psr.textVec, err = s.postgres.VectorSearchWithCutoff(ctx, queryVec, p.UserName, TextScopes, budget.textK, cutoffISO, p.AgentID)
		case len(p.CubeIDs) > 1:
			psr.textVec, err = s.postgres.VectorSearchMultiCube(ctx, queryVec, p.CubeIDs, TextScopes, p.AgentID, budget.textK)
		default:
			psr.textVec, err = s.postgres.VectorSearch(ctx, queryVec, p.UserName, TextScopes, p.AgentID, budget.textK)
		}
		return err
	})
	if tsquery != "" {
		g.Go(func() error {
			var err error
			if hasCutoff {
				psr.textFT, err = s.postgres.FulltextSearchWithCutoff(ctx, tsquery, p.UserName, TextScopes, budget.textK, cutoffISO, p.AgentID)
			} else {
				psr.textFT, err = s.postgres.FulltextSearch(ctx, tsquery, p.UserName, TextScopes, p.AgentID, budget.textK)
			}
			return err
		})
	}
}

// spawnSkillToolSearches enqueues skill and tool vector/fulltext search goroutines.
func (s *SearchService) spawnSkillToolSearches(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults,
	queryVec []float32, tsquery string, p SearchParams, budget searchBudget,
) {
	if p.IncludeSkill && p.SkillTopK > 0 {
		g.Go(func() error {
			var err error
			psr.skillVec, err = s.postgres.VectorSearch(ctx, queryVec, p.UserName, SkillScopes, p.AgentID, budget.skillK)
			return err
		})
		if tsquery != "" {
			g.Go(func() error {
				var err error
				psr.skillFT, err = s.postgres.FulltextSearch(ctx, tsquery, p.UserName, SkillScopes, p.AgentID, budget.skillK)
				return err
			})
		}
	}
	if p.IncludeTool && p.ToolTopK > 0 {
		g.Go(func() error {
			var err error
			psr.toolVec, err = s.postgres.VectorSearch(ctx, queryVec, p.UserName, ToolScopes, p.AgentID, budget.toolK)
			return err
		})
		if tsquery != "" {
			g.Go(func() error {
				var err error
				psr.toolFT, err = s.postgres.FulltextSearch(ctx, tsquery, p.UserName, ToolScopes, p.AgentID, budget.toolK)
				return err
			})
		}
	}
}

// spawnPrefSearch enqueues the Qdrant preference search goroutine if applicable.
func (s *SearchService) spawnPrefSearch(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults,
	queryVec []float32, p SearchParams, budget searchBudget,
) {
	if s.qdrant == nil || !p.IncludePref || p.PrefTopK <= 0 {
		return
	}
	g.Go(func() error {
		for _, coll := range PrefCollections {
			results, err := s.qdrant.SearchByVector(ctx, coll, queryVec, uint64(budget.prefK), p.UserName) //nolint:gosec // prefK is a small positive search top-k value
			if err != nil {
				s.logger.Debug("pref search failed", slog.String("collection", coll), slog.Any("error", err))
				continue
			}
			psr.prefResults = append(psr.prefResults, results...)
		}
		return nil
	})
}

// spawnWorkingMemAndGraph enqueues working-memory fetch and all graph recall goroutines.
func (s *SearchService) spawnWorkingMemAndGraph(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults,
	tokens []string, p SearchParams,
) {
	g.Go(func() error {
		var err error
		psr.workingMemItems, err = s.postgres.GetWorkingMemory(ctx, p.UserName, WorkingMemoryLimit, p.AgentID)
		if err != nil {
			s.logger.Debug("working memory fetch failed", slog.Any("error", err))
		}
		return nil
	})
	if len(tokens) > 0 {
		g.Go(func() error {
			var err error
			psr.graphKeyResults, err = s.postgres.GraphRecallByKey(ctx, p.UserName, GraphRecallScopes, tokens, p.AgentID, GraphRecallLimit)
			if err != nil {
				s.logger.Debug("graph recall by key failed", slog.Any("error", err))
			}
			return nil
		})
		g.Go(func() error {
			normalized := make([]string, len(tokens))
			for i, t := range tokens {
				normalized[i] = db.NormalizeEntityID(t)
			}
			entityIDs, err := s.postgres.FindEntitiesByNormalizedID(ctx, normalized, p.UserName)
			if err != nil || len(entityIDs) == 0 {
				return nil
			}
			psr.entityGraphResults, err = s.postgres.GetMemoriesByEntityIDs(ctx, entityIDs, p.UserName, GraphRecallLimit)
			if err != nil {
				s.logger.Debug("entity graph recall failed", slog.Any("error", err))
			}
			return nil
		})
	}
	if len(tokens) >= 2 {
		g.Go(func() error {
			var err error
			psr.graphTagResults, err = s.postgres.GraphRecallByTags(ctx, p.UserName, GraphRecallScopes, tokens, p.AgentID, GraphRecallLimit)
			if err != nil {
				s.logger.Debug("graph recall by tags failed", slog.Any("error", err))
			}
			return nil
		})
	}
}

// spawnInternetSearch enqueues an internet search goroutine if enabled.
func (s *SearchService) spawnInternetSearch(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults, p SearchParams,
) {
	if s.Internet == nil || !p.InternetSearch {
		return
	}
	g.Go(func() error {
		var err error
		psr.internetResults, err = s.Internet.Search(ctx, p.Query)
		if err != nil {
			s.logger.Debug("internet search failed", slog.Any("error", err))
		}
		return nil // never fail the pipeline
	})
}

// embedInternetResults converts internet search results into MergedResults with embeddings.
func (s *SearchService) embedInternetResults(ctx context.Context, results []InternetResult) []MergedResult {
	if len(results) == 0 || s.embedder == nil {
		return nil
	}
	texts := make([]string, len(results))
	for i, r := range results {
		texts[i] = r.Text()
	}
	vecs, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		s.logger.Debug("internet embed failed", slog.Any("error", err))
		return nil
	}
	merged := make([]MergedResult, 0, len(results))
	for i, r := range results {
		if i >= len(vecs) {
			break
		}
		merged = append(merged, MergedResult{
			ID: "internet:" + r.URL,
			Properties: fmt.Sprintf(
				`{"memory": %q, "memory_type": "InternetMemory", "sources": [{"url": %q, "title": %q}]}`,
				r.Text(), r.URL, r.Title,
			),
			Score:     InternetBaseScore,
			Embedding: vecs[i],
		})
	}
	return merged
}

// runBFSExpansion runs graph BFS traversal from top vector hits.
func (s *SearchService) runBFSExpansion(ctx context.Context, textVec []db.VectorSearchResult, p SearchParams) []db.GraphRecallResult {
	if len(textVec) == 0 {
		return nil
	}
	seedN := 5
	if len(textVec) < seedN {
		seedN = len(textVec)
	}
	seedIDs := make([]string, 0, seedN)
	for _, r := range textVec[:seedN] {
		seedIDs = append(seedIDs, r.ID)
	}
	bfs, err := s.postgres.GraphBFSTraversal(ctx, seedIDs, p.UserName, GraphRecallScopes, 2, GraphRecallLimit, p.AgentID)
	if err != nil {
		s.logger.Debug("graph bfs traversal failed", slog.Any("error", err))
		return nil
	}
	return bfs
}

// mergeSearchResults merges all parallel results into per-type slices.
func (s *SearchService) mergeSearchResults(psr *parallelSearchResults, bfsResults []db.GraphRecallResult, internetMerged []MergedResult, p SearchParams) (textMerged, skillMerged, toolMerged []MergedResult) {
	textMerged = MergeVectorAndFulltext(psr.textVec, psr.textFT)
	skillMerged = MergeVectorAndFulltext(psr.skillVec, psr.skillFT)
	toolMerged = MergeVectorAndFulltext(psr.toolVec, psr.toolFT)

	graphAll := slices.Concat(psr.graphKeyResults, psr.graphTagResults, bfsResults, psr.entityGraphResults)
	if len(graphAll) == 0 {
		return textMerged, skillMerged, toolMerged
	}

	var graphText, graphSkill []db.GraphRecallResult
	for _, g := range graphAll {
		props := ParseProperties(g.Properties)
		if props == nil {
			continue
		}
		mtype, _ := props["memory_type"].(string)
		if mtype == "SkillMemory" {
			graphSkill = append(graphSkill, g)
		} else {
			graphText = append(graphText, g)
		}
	}
	textMerged = MergeGraphIntoResults(textMerged, graphText)
	if p.IncludeSkill && p.SkillTopK > 0 {
		skillMerged = MergeGraphIntoResults(skillMerged, graphSkill)
	}
	textMerged = append(textMerged, internetMerged...)
	return textMerged, skillMerged, toolMerged
}

// applyContradictsPenalty lowers scores of memories contradicted by higher-ranked results.
func (s *SearchService) applyContradictsPenalty(ctx context.Context, textMerged []MergedResult, p SearchParams) []MergedResult {
	if len(textMerged) == 0 {
		return textMerged
	}
	seedN := 10
	if len(textMerged) < seedN {
		seedN = len(textMerged)
	}
	seedIDs := make([]string, 0, seedN)
	for _, r := range textMerged[:seedN] {
		seedIDs = append(seedIDs, r.ID)
	}
	contradicted, err := s.postgres.GraphRecallByEdge(ctx, seedIDs, db.EdgeContradicts, p.UserName, contradictsEdgeSeedN)
	if err != nil || len(contradicted) == 0 {
		return textMerged
	}
	contradictedSet := make(map[string]bool, len(contradicted))
	for _, c := range contradicted {
		contradictedSet[c.ID] = true
	}
	return PenalizeContradicts(textMerged, contradictedSet)
}

// runIterativeExpansion applies iterative multi-stage retrieval if configured.
func (s *SearchService) runIterativeExpansion(ctx context.Context, queryVec []float32, textFormatted []map[string]any, p SearchParams) []map[string]any {
	numStages := p.NumStages
	if numStages <= 0 || s.Iterative.APIURL == "" || s.embedder == nil || s.postgres == nil {
		return textFormatted
	}
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
	result := IterativeExpand(ctx, p.Query, textFormatted, embedFn, extCfg)
	s.logger.Debug("iterative expansion complete",
		slog.String("query", p.Query),
		slog.Int("total_items", len(result)),
	)
	return result
}

// applyTemporalDecay applies temporal decay to all formatted result slices.
func (s *SearchService) applyTemporalDecay(text, skill, tool []map[string]any, p SearchParams) ([]map[string]any, []map[string]any, []map[string]any) {
	decayAlpha := p.DecayAlpha
	if decayAlpha == 0 {
		decayAlpha = DefaultDecayAlpha
	}
	if decayAlpha <= 0 {
		return text, skill, tool
	}
	now := time.Now()
	return ApplyTemporalDecay(text, now, decayAlpha),
		ApplyTemporalDecay(skill, now, decayAlpha),
		ApplyTemporalDecay(tool, now, decayAlpha)
}

// applyRelativity filters all result slices by the relativity threshold.
func (s *SearchService) applyRelativity(text, skill, tool, pref []map[string]any, p SearchParams) ([]map[string]any, []map[string]any, []map[string]any, []map[string]any) {
	if p.Relativity <= 0 {
		return text, skill, tool, pref
	}
	text = FilterByRelativity(text, p.Relativity)
	skill = FilterByRelativity(skill, p.Relativity)
	tool = FilterByRelativity(tool, p.Relativity)
	prefThreshold := p.Relativity - 0.10
	if prefThreshold > 0 {
		pref = FilterByRelativity(pref, prefThreshold)
	}
	return text, skill, tool, pref
}

// dedupResults applies the requested dedup strategy to all result slices.
func (s *SearchService) dedupResults(queryVec []float32, textEmbByID map[string][]float32, text, skill, tool, pref []map[string]any, p SearchParams) ([]map[string]any, []map[string]any, []map[string]any, []map[string]any) {
	switch p.Dedup {
	case DedupModeSim:
		textItems := ToSearchItems(text, textEmbByID, "text")
		textItems = DedupSim(textItems, p.TopK)
		text = FromSearchItems(textItems)
		skill = DedupByText(skill)
		tool = DedupByText(tool)
		pref = DedupByText(pref)

	case DedupModeMMR:
		textItems := ToSearchItems(text, textEmbByID, "text")
		prefItems := ToSearchItems(pref, nil, "preference")
		combined := slices.Concat(textItems, prefItems)
		if len(combined) > 0 {
			mmrLambda := p.MMRLambda
			if mmrLambda <= 0 || mmrLambda > 1 {
				mmrLambda = DefaultMMRLambda
			}
			dedupedText, dedupedPref := DedupMMR(combined, p.TopK, p.PrefTopK, queryVec, mmrLambda)
			text = FromSearchItems(dedupedText)
			pref = FromSearchItems(dedupedPref)
		}
		skill = DedupByText(skill)
		tool = DedupByText(tool)

	default:
		text = DedupByText(text)
		skill = DedupByText(skill)
		tool = DedupByText(tool)
		pref = DedupByText(pref)
	}
	return text, skill, tool, pref
}

// postProcessResults runs steps 6–11 of the search pipeline:
// cosine rerank → LLM rerank (opt-in) → iterative expansion → temporal decay →
// relativity threshold → pref quality filter → dedup → cross-source dedup → trim.
// Returns the processed slices plus timing durations for llm_rerank and iterative steps.
func (s *SearchService) postProcessResults(
	ctx context.Context,
	queryVec []float32,
	textEmbByID, skillEmbByID, toolEmbByID map[string][]float32,
	text, skill, tool, pref []map[string]any,
	p SearchParams,
) (retText, retSkill, retTool, retPref []map[string]any, llmRerankDur, iterativeDur time.Duration) {
	// Step 6: Cosine rerank
	text = ReRankByCosine(queryVec, text, textEmbByID)
	skill = ReRankByCosine(queryVec, skill, skillEmbByID)
	tool = ReRankByCosine(queryVec, tool, toolEmbByID)

	// Step 6.1: LLM rerank of text_mem (adaptive strategy)
	if decision := rerankStrategy(text); p.LLMRerank && s.LLMReranker.APIURL != "" && decision.ShouldRerank {
		t0 := time.Now()
		rerankInput := text
		if decision.TopK > 0 && decision.TopK < len(text) {
			rerankInput = text[:decision.TopK]
		}
		reranked := LLMRerank(ctx, p.Query, rerankInput, s.LLMReranker)
		if decision.TopK > 0 && decision.TopK < len(text) {
			text = append(reranked, text[decision.TopK:]...)
		} else {
			text = reranked
		}
		llmRerankDur = time.Since(t0)
	}

	// Step 6.2: Iterative multi-stage retrieval expansion
	t0 := time.Now()
	text = s.runIterativeExpansion(ctx, queryVec, text, p)
	iterativeDur = time.Since(t0)

	// Step 6.5: Temporal decay
	text, skill, tool = s.applyTemporalDecay(text, skill, tool, p)

	// Step 7: Relativity threshold
	text, skill, tool, pref = s.applyRelativity(text, skill, tool, pref, p)

	// Step 8: Pref quality filter
	pref = FilterPrefByQuality(pref)

	// Step 9: Dedup per type
	text, skill, tool, pref = s.dedupResults(queryVec, textEmbByID, text, skill, tool, pref, p)

	// Step 10: Cross-source dedup
	skill, tool, pref = CrossSourceDedupByText(text, skill, tool, pref)

	// Step 11: Trim each type to its budget
	text = TrimSlice(text, p.TopK)
	skill = TrimSlice(skill, p.SkillTopK)
	tool = TrimSlice(tool, p.ToolTopK)
	pref = TrimSlice(pref, p.PrefTopK)

	StripEmbeddings(text)
	StripEmbeddings(skill)
	StripEmbeddings(tool)
	StripEmbeddings(pref)

	return text, skill, tool, pref, llmRerankDur, iterativeDur
}

// formatWorkingMem converts working memory items to formatted act_mem entries.
func (s *SearchService) formatWorkingMem(queryVec []float32, items []db.VectorSearchResult, p SearchParams) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	wmMerged := make([]MergedResult, 0, len(items))
	for _, r := range items {
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
		threshold := p.Relativity - 0.10
		if threshold > 0 {
			actFormatted = FilterByRelativity(actFormatted, threshold)
		}
	}
	actFormatted = TrimSlice(actFormatted, WorkingMemoryLimit)
	StripEmbeddings(actFormatted)
	return actFormatted
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
