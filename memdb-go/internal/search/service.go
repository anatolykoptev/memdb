// Package search — unified SearchService used by both REST and MCP handlers.
// This file contains the SearchService struct, constructor, and the top-level
// Search() orchestrator. Concern-specific helpers live in:
//   - service_types.go      : postgresClient, SearchParams, parallelSearchResults, searchBudget
//   - service_parallel.go   : parallel DB fetch + BFS/internet augmentation
//   - service_merge.go      : merge per type + CONTRADICTS penalty
//   - service_postprocess.go: rerank / filter / dedup (steps 6–11)
//   - service_response.go   : working-memory formatting + response build
//   - service_fine.go       : fine-mode orchestration
package search

import (
	"context"
	"log/slog"
	"time"

	"github.com/anatolykoptev/go-kit/rerank"
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/scheduler"
)

// SearchService performs the full search pipeline: embed → parallel DB queries →
// merge → format → rerank → filter → dedup → trim → build response.
type SearchService struct {
	postgres    postgresClient
	qdrant      *db.Qdrant
	embedder    embedder.Embedder
	logger      *slog.Logger
	LLMReranker LLMRerankConfig
	// RerankClient is the cross-encoder rerank client (step 6.05). Nil-safe:
	// a nil or disabled client returns Available()=false. Constructed in
	// server_init_search.go via rerank.New(...).
	RerankClient *rerank.Client
	Iterative    IterativeConfig
	Enhance      EnhanceConfig
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
// pg must satisfy postgresClient; the concrete *db.Postgres does.
func NewSearchService(pg *db.Postgres, qd *db.Qdrant, emb embedder.Embedder, logger *slog.Logger) *SearchService {
	s := &SearchService{
		qdrant:   qd,
		embedder: emb,
		logger:   logger,
	}
	// Assign only when non-nil to keep the interface nil when no postgres is provided,
	// so s.postgres != nil checks remain correct (typed-nil-in-interface pitfall).
	if pg != nil {
		s.postgres = pg
	}
	return s
}

// CanSearch returns true if the minimum dependencies (embedder + postgres) are available.
func (s *SearchService) CanSearch() bool {
	return s.embedder != nil && s.postgres != nil
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
	var llmRerankDur, iterativeDur, ceRerankDur time.Duration
	textFormatted, skillFormatted, toolFormatted, prefFormatted, llmRerankDur, iterativeDur, ceRerankDur =
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
		slog.Duration("ce_rerank", ceRerankDur),
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
