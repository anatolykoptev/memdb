// Package search — service_types.go: SearchService-adjacent type declarations.
// Kept separate from service.go so the orchestrator file stays focused on the
// pipeline entry point.
package search

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// postgresClient is the subset of db.Postgres methods used by SearchService.
// Introducing the interface enables unit tests to inject a mock without a real
// Postgres connection. The concrete *db.Postgres satisfies this interface.
type postgresClient interface {
	VectorSearch(ctx context.Context, vector []float32, cubeID, personID string, memoryTypes []string, agentID string, limit int) ([]db.VectorSearchResult, error)
	VectorSearchMultiCube(ctx context.Context, vector []float32, cubeIDs []string, personID string, memoryTypes []string, agentID string, limit int) ([]db.VectorSearchResult, error)
	VectorSearchWithCutoff(ctx context.Context, vector []float32, cubeID, personID string, memoryTypes []string, limit int, cutoff string, agentID string) ([]db.VectorSearchResult, error)
	FulltextSearch(ctx context.Context, tsquery string, cubeID, personID string, memoryTypes []string, agentID string, limit int) ([]db.VectorSearchResult, error)
	FulltextSearchWithCutoff(ctx context.Context, tsquery string, cubeID, personID string, memoryTypes []string, limit int, cutoff string, agentID string) ([]db.VectorSearchResult, error)
	GetWorkingMemory(ctx context.Context, cubeID, personID string, limit int, agentID string) ([]db.VectorSearchResult, error)
	GraphRecallByKey(ctx context.Context, cubeID, personID string, memoryTypes []string, keys []string, agentID string, limit int) ([]db.GraphRecallResult, error)
	GraphRecallByTags(ctx context.Context, cubeID, personID string, memoryTypes []string, tags []string, agentID string, limit int) ([]db.GraphRecallResult, error)
	GraphRecallByEdge(ctx context.Context, seedIDs []string, relation, cubeID, personID string, limit int) ([]db.GraphRecallResult, error)
	GraphBFSTraversal(ctx context.Context, seedIDs []string, cubeID, personID string, memoryTypes []string, depth, limit int, agentID string) ([]db.GraphRecallResult, error)
	MultiHopEdgeExpansion(ctx context.Context, seedIDs []string, cubeID, personID string, depth, limit int, agentID string) ([]db.GraphExpansion, error)
	FindEntitiesByNormalizedID(ctx context.Context, normalizedIDs []string, cubeID, personID string) ([]string, error)
	GetMemoriesByEntityIDs(ctx context.Context, entityIDs []string, cubeID, personID string, limit int) ([]db.GraphRecallResult, error)
	IncrRetrievalCount(ctx context.Context, ids []string, now string) error
}

// contradictsEdgeSeedN is the number of top results used as seed IDs
// for the CONTRADICTS edge recall step.
const contradictsEdgeSeedN = 20

// Dedup mode values for SearchParams.Dedup.
const (
	DedupModeSim = "sim" // similarity-based deduplication
	DedupModeMMR = "mmr" // maximal marginal relevance deduplication
	DedupModeNo  = "no"  // no deduplication
)

// Level selects which MemOS memory tier(s) to query.
// Empty string means "all" (full search, backward-compat default).
type Level string

const (
	LevelAll Level = ""   // full search — default, backward-compat
	LevelL1  Level = "l1" // working memory only (postgres WorkingMemory rows)
	LevelL2  Level = "l2" // episodic only  (Postgres Memory where memory_type='EpisodicMemory')
	LevelL3  Level = "l3" // LTM full graph (Postgres LongTermMemory + UserMemory + EpisodicMemory + graph)
)

// ParseLevel parses a raw level string; returns LevelAll for "".
// Returns an error for any unrecognised value.
func ParseLevel(s string) (Level, error) {
	switch Level(s) {
	case LevelAll, LevelL1, LevelL2, LevelL3:
		return Level(s), nil
	default:
		return LevelAll, fmt.Errorf("invalid level %q: must be l1, l2, or l3 (or omit for full search)", s)
	}
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
	// Level restricts the search to a specific MemOS memory tier.
	// LevelAll (empty string, the zero value) = full search (backward compat).
	// LevelL1 = working memory only; LevelL2 = episodic only; LevelL3 = LTM full graph.
	Level Level
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
