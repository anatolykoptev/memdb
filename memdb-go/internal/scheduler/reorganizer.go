package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	// dupThreshold is the cosine similarity above which two memories are
	// considered near-duplicates and sent for LLM consolidation.
	dupThreshold = 0.85

	// dupScanLimit caps the number of pairs returned by FindNearDuplicates
	// to bound LLM calls per reorganization cycle.
	dupScanLimit = 60

	// reorganizerLLMTimeout is the per-cluster LLM call deadline.
	reorganizerLLMTimeout = 45 * time.Second

	// wmRefreshMinScore is the minimum cosine similarity for LTM nodes to be
	// promoted into WorkingMemory during a mem_update refresh.
	// Lower than dupThreshold — we want relevant context, not just near-duplicates.
	wmRefreshMinScore = 0.60

	// wmRefreshTopK is the maximum number of LTM nodes to add to WM per refresh.
	// Mirrors Python's top_k=10 in process_session_turn.
	wmRefreshTopK = 10

	// wmRefreshEmbedTimeout is the deadline for embedding the query in RefreshWorkingMemory.
	wmRefreshEmbedTimeout = 10 * time.Second

	// importanceDecayFactor is the multiplier applied to importance_score each periodic cycle.
	// 0.95 per 6h ≈ 0.70 after 24h, 0.13 after 7 days without retrieval → natural fade.
	importanceDecayFactor = 0.95

	// importanceArchiveThreshold is the importance_score below which a memory is auto-archived.
	// A memory starting at 1.0 reaches this threshold after ~44 decay cycles (≈11 days).
	// Each retrieval boosts by 0.1, so actively recalled memories stay well above threshold.
	importanceArchiveThreshold = 0.10

	// llmCompactMaxTokens is the max token budget for LLM calls that produce compact JSON output
	// (memory enhancement, preference extraction, WM compaction). Larger calls use their own constant.
	llmCompactMaxTokens = 512

	// llmTruncateLen is the max characters of raw LLM output shown in error messages.
	llmTruncateLen = 200
)

// Reorganizer detects near-duplicate LongTermMemory/UserMemory nodes for a
// given cube, calls the LLM to consolidate each cluster, and persists the result.
//
// Algorithm (based on redis/agent-memory-server + MemOS best practices):
//  1. FindNearDuplicates → high-cosine pairs from Postgres pgvector
//  2. Union-Find → group pairs into clusters of related memories
//  3. Per cluster: one LLM call → keep_id + remove_ids + merged_text
//  4. UpdateMemoryNodeFull(keep_id, merged_text, new_embedding)
//  5. Soft-delete all remove_ids
//  6. Evict remove_ids from Redis VSET hot cache
type Reorganizer struct {
	postgres     *db.Postgres
	embedder     embedder.Embedder
	wmCache      *db.WorkingMemoryCache // nil = VSET not configured
	llmClient    *llm.Client            // shared LLM client with retry + fallback
	logger       *slog.Logger
	llmExtractor *llm.LLMExtractor // for ExtractAndDedup (fine-level mem_read)
	profiler     *Profiler         // for TriggerRefresh after mem_read
}

// SetLLMExtractor injects the LLM extractor for fine-level mem_read processing.
func (r *Reorganizer) SetLLMExtractor(e *llm.LLMExtractor) { r.llmExtractor = e }

// SetProfiler injects the profiler for background user profile refresh.
func (r *Reorganizer) SetProfiler(p *Profiler) { r.profiler = p }

// NewReorganizer creates a Reorganizer. llmClient provides retry + model
// fallback for all LLM calls (consolidation, feedback, prefs, enhance).
func NewReorganizer(
	postgres *db.Postgres,
	emb embedder.Embedder,
	wmCache *db.WorkingMemoryCache,
	llmClient *llm.Client,
	logger *slog.Logger,
) *Reorganizer {
	return &Reorganizer{
		postgres:  postgres,
		embedder:  emb,
		wmCache:   wmCache,
		llmClient: llmClient,
		logger:    logger,
	}
}

// DecayAndArchive runs the two-phase importance lifecycle for a cube:
//  1. Multiply importance_score * importanceDecayFactor for all LTM/UserMemory
//  2. Auto-archive nodes whose score fell below importanceArchiveThreshold
//
// Returns the number of nodes archived in this cycle.
// Non-fatal: called from periodicReorgLoop; errors are logged by the caller.
func (r *Reorganizer) DecayAndArchive(ctx context.Context, cubeID string) (int64, error) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000")
	archived, err := r.postgres.DecayAndArchiveImportance(ctx, cubeID, importanceDecayFactor, importanceArchiveThreshold, now)
	if err != nil {
		return 0, err
	}
	if archived > 0 {
		r.logger.Info("importance decay: archived low-importance memories",
			slog.String("cube_id", cubeID),
			slog.Int64("archived", archived),
		)
	}
	return archived, nil
}
