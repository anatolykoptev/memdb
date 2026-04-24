package db

// postgres_memory.go — shared types for memory node operations.
// Concrete methods live in postgres_memory_users.go, _crud.go, _wm.go, _ltm.go, _add.go, _admin.go.

// MemNode is a lightweight struct for id+memory text retrieval.
type MemNode struct {
	ID   string
	Text string
}

// MemoryInsertNode holds the data for inserting a single memory node.
type MemoryInsertNode struct {
	ID             string // properties->>'id' (UUID string)
	PropertiesJSON []byte // marshaled JSONB
	EmbeddingVec   string // "[0.1,0.2,...]" for pgvector cast
}

// DuplicatePair represents two semantically similar memory nodes found by vector search.
type DuplicatePair struct {
	IDa   string
	MemA  string
	IDb   string
	MemB  string
	Score float64 // cosine similarity in [0, 1]
}

// WMNode is a WorkingMemory node returned by GetRecentWorkingMemory.
type WMNode struct {
	ID        string
	Text      string
	TS        int64
	Embedding []float32
}

// LTMSearchResult is one result from SearchLTMByVector.
type LTMSearchResult struct {
	ID        string
	Text      string
	Score     float64
	Embedding []float32 // node's own embedding — use this for VSET VAdd, not the query embedding
}

// RawMemory is a memory node identified as a raw conversation window (fast-mode artifact).
type RawMemory struct {
	ID     string // properties->>'id' (UUID)
	Memory string // raw conversation text
}

// HierarchyMemory is a lightweight memory row fetched by ListMemoriesByHierarchyLevel.
// Used by the D3 TreeManager to batch-load candidates for clustering and LLM
// consolidation per tier. Embedding is parsed from the text representation so
// callers can use it for local cosine clustering without re-embedding.
type HierarchyMemory struct {
	ID        string
	Text      string
	UserID    string
	Embedding []float32
}
