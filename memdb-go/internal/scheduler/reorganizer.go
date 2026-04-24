package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// reorgPostgres is the narrow Postgres surface used by the Reorganizer.
// Using an interface (rather than *db.Postgres directly) makes the Reorganizer
// testable with spy/stub implementations without a live database.
type reorgPostgres interface {
	// Near-duplicate detection (legacy O(N²) and HNSW-indexed variants)
	FindNearDuplicates(ctx context.Context, userName string, threshold float64, limit int) ([]db.DuplicatePair, error)
	FindNearDuplicatesByIDs(ctx context.Context, userName string, ids []string, threshold float64, limit int) ([]db.DuplicatePair, error)
	FindNearDuplicatesHNSW(ctx context.Context, userName string, threshold float64, limit, topK int) ([]db.DuplicatePair, error)
	FindNearDuplicatesHNSWByIDs(ctx context.Context, userName string, ids []string, threshold float64, limit, topK int) ([]db.DuplicatePair, error)

	// Memory node lifecycle
	InsertMemoryNodes(ctx context.Context, nodes []db.MemoryInsertNode) error
	UpdateMemoryNodeFull(ctx context.Context, memoryID, newText, embeddingVec, updatedAt string) error
	SoftDeleteMerged(ctx context.Context, memoryID, mergedIntoID, updatedAt string) error
	DeleteByPropertyIDs(ctx context.Context, propertyIDs []string, userName string) (int64, error)

	// Graph edges
	CreateMemoryEdge(ctx context.Context, fromID, toID, relation, createdAt, validAt string) error
	InvalidateEdgesByMemoryID(ctx context.Context, memoryID, invalidAt string) error
	InvalidateEntityEdgesByMemoryID(ctx context.Context, memoryID, invalidAt string) error

	// Entity graph
	UpsertEntityNodeWithEmbedding(ctx context.Context, name, entityType, userName, now, embVec string) (string, error)
	UpsertEntityEdge(ctx context.Context, fromEntityID, predicate, toEntityID, memoryID, userName, validAt, createdAt string) error

	// Memory reads
	GetMemoryByPropertyIDs(ctx context.Context, ids []string, userName string) ([]db.MemNode, error)
	GetMemoriesByPropertyIDs(ctx context.Context, ids []string) ([]map[string]any, error)
	FilterExistingContentHashes(ctx context.Context, hashes []string, userName string) (map[string]bool, error)
	VectorSearch(ctx context.Context, vector []float32, cubeID, personID string, memoryTypes []string, agentID string, limit int) ([]db.VectorSearchResult, error)

	// WorkingMemory
	SearchLTMByVector(ctx context.Context, userName, embeddingVec string, minScore float64, limit int) ([]db.LTMSearchResult, error)
	CountWorkingMemory(ctx context.Context, userName string) (int64, error)
	GetWorkingMemoryOldestFirst(ctx context.Context, userName string, limit int) ([]db.MemNode, error)

	// Importance lifecycle
	DecayAndArchiveImportance(ctx context.Context, userName string, decayFactor, archiveThreshold float64, now string) (int64, error)

	// D3 hierarchy — batch-load memories at a given tier for clustering + LLM consolidation.
	ListMemoriesByHierarchyLevel(ctx context.Context, userName, level string, limit int) ([]db.HierarchyMemory, error)
	// D3 relation detector — edge insert with confidence + rationale.
	CreateMemoryEdgeWithConfidence(ctx context.Context, fromID, toID, relation, createdAt, validAt string, confidence float64, rationale string) error
	// D3 history audit trail.
	InsertTreeConsolidationEvent(ctx context.Context, eventID, cubeID, parentID string, childIDs []string, tier, llmModel, promptSHA, createdAt string) error
	// D3 — promote a memory into a higher tier (sets hierarchy_level + parent_memory_id
	// on parent_memory_id for children; sets hierarchy_level on the parent itself).
	SetHierarchyLevel(ctx context.Context, memoryID, level, parentMemoryID, updatedAt string) error
}

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
	// 30s to handle ARM under load (e5-large ONNX can spike to 10s+ with CPU contention).
	wmRefreshEmbedTimeout = 30 * time.Second

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
	postgres         reorgPostgres          // *db.Postgres in production, spy in tests
	embedder         embedder.Embedder
	wmCache          *db.WorkingMemoryCache // nil = VSET not configured
	llmClient        *llm.Client            // shared LLM client with retry + fallback
	logger           *slog.Logger
	llmExtractor     *llm.LLMExtractor // for ExtractAndDedup (fine-level mem_read)
	profiler         *Profiler         // for TriggerRefresh after mem_read
	cacheInvalidator CacheInvalidator  // nil = cache invalidation disabled
	useHNSW          bool              // route FindNearDuplicates through HNSW index
}

// SetLLMExtractor injects the LLM extractor for fine-level mem_read processing.
func (r *Reorganizer) SetLLMExtractor(e *llm.LLMExtractor) { r.llmExtractor = e }

// SetProfiler injects the background user profile summarizer.
func (r *Reorganizer) SetProfiler(p *Profiler) { r.profiler = p }

// SetCacheInvalidator injects the Redis cache invalidator.
// When set, Reorganizer will purge stale search-result cache entries after
// hard-deleting or soft-deleting memory nodes, preventing clients from
// receiving stale IDs in subsequent search responses.
func (r *Reorganizer) SetCacheInvalidator(c CacheInvalidator) { r.cacheInvalidator = c }

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

// SetUseHNSW enables the HNSW-indexed FindNearDuplicates path.
// Default false (legacy O(N²) self-join). Set via cfg.ReorgUseHNSW.
func (r *Reorganizer) SetUseHNSW(v bool) { r.useHNSW = v }

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
