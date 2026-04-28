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
	// CoTDecomposer is the D11 multi-hop / temporal query decomposer. nil =
	// disabled (the default and the zero-regression path). When non-nil,
	// applyCoTDecomposition fans sub-queries through extra text-scope vector
	// probes BEFORE the D2 expandViaGraph step so the graph walk sees a
	// richer seed set.
	CoTDecomposer *CoTDecomposer
	// pprScorer computes Personalized PageRank for F14 (HippoRAG 2). nil = disabled.
	// Wire in via SetPPRScorer (ppr_scorer.go). The concrete type is *scheduler.Worker
	// but we hold the interface to avoid import cycles.
	pprScorer PPRScorer
	// Reflection is the F2 reflection-loop deep-search agent. nil = disabled
	// (also disabled if MEMDB_F2_REFLECTION env is unset/false). Wired via
	// SetReflectionAgent so server_init_search.go can construct it lazily
	// from the same llm.Client used by the rest of the search service.
	Reflection *ReflectionAgent
}

// SetReflectionAgent installs the F2 reflection agent. Pass nil to disable.
// Safe to call once at startup before Search is invoked.
func (s *SearchService) SetReflectionAgent(a *ReflectionAgent) {
	s.Reflection = a
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

// Search executes the full native search pipeline as a sequence of stages
// (see pipeline.go / pipeline_*.go). Per-stage timings are emitted as
// memdb.search.stage_duration_ms / stage_total — the legacy "search
// pipeline timing" slog line is preserved for backward compatibility.
//
// Only embed_query is fatal — every other stage soft-fails. The
// orchestrator inspects state.embedErr after runPipeline to surface that
// one specific error to callers (existing contract).
func (s *SearchService) Search(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	pipelineStart := time.Now()
	st := &pipelineState{Params: p, EmbedQuery: p.Query}
	stages := s.defaultStages()

	runPipeline(ctx, s.logger, stages, st)

	// embed_query is the only fatal stage — surface its error verbatim.
	if st.embedErr != nil {
		return nil, st.embedErr
	}

	// Backward-compat slog line — operators have dashboards keyed off it.
	s.logger.Info("search pipeline timing",
		slog.Duration("total", time.Since(pipelineStart)),
		slog.Duration("embed", st.Timings["embed_query"]),
		slog.Duration("parallel_db", st.Timings["parallel_db_fanout"]),
		slog.Duration("cot_augment", st.Timings["d7_cot_augment"]),
		slog.Duration("d11_decompose", st.Timings["d11_cot_decompose"]),
		slog.Int("d11_subqueries", len(st.D11Subqueries)),
		slog.Duration("bfs", st.Timings["bfs_expand"]),
		slog.Duration("multihop", st.Timings["d2_graph_expand"]),
		slog.Duration("contradicts", st.Timings["contradicts_penalty"]),
		slog.Duration("ce_rerank", st.Timings["ce_rerank"]),
		slog.Duration("llm_rerank", st.Timings["llm_rerank"]),
		slog.Duration("iterative", st.Timings["iterative"]),
		slog.Duration("post_process", st.Timings["post_process"]),
		slog.Duration("profile", st.Timings["profile_inject"]),
	)

	return &SearchOutput{Result: st.Result}, nil
}

// defaultStages returns the canonical search pipeline. New features (F1
// VEC_COT, F2 reflection-loop, F6) plug in by inserting a stage at the
// right point — no edits to Search() needed. Stage order MUST match
// stageNames in pipeline.go (used for metric pre-registration).
func (s *SearchService) defaultStages() []stage {
	return []stage{
		funcStage{"d7_cot_decompose", s.stageD7CoTDecompose},
		funcStage{"d4_query_rewrite", s.stageD4QueryRewrite},
		funcStage{"embed_query", s.stageEmbedQuery},
		funcStage{"tokenize_temporal_cutoff", s.stageTokenizeTemporal},
		funcStage{"parallel_db_fanout", s.stageParallelDB},
		funcStage{"d7_cot_augment", s.stageD7CoTAugment},
		funcStage{"d11_cot_decompose", s.stageD11CoTDecompose},
		funcStage{"bfs_expand", s.stageBFSExpand},
		funcStage{"internet_embed", s.stageInternetEmbed},
		funcStage{"merge_candidates", s.stageMergeCandidates},
		funcStage{"d2_graph_expand", s.stageD2GraphExpand},
		funcStage{"contradicts_penalty", s.stageContradictsPenalty},
		funcStage{"format_items", s.stageFormatItems},
		funcStage{"post_process", s.stagePostProcess},
		funcStage{"reflect", s.stageReflect},
		funcStage{"working_mem_format", s.stageWorkingMemFormat},
		funcStage{"build_response", s.stageBuildResponse},
		funcStage{"profile_inject", s.stageProfileInject},
		funcStage{"retrieval_count_async", s.stageRetrievalCountAsync},
	}
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
