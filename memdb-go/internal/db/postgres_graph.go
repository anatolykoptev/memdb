package db

// postgres_graph.go — graph recall and memory_edges operations.
// Covers: GraphRecallByKey, GraphRecallByTags, GraphBFSTraversal,
// memory_edges table management, CreateMemoryEdge, GraphRecallByEdge,
// FilterExistingContentHashes.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// GraphRecallResult holds a single result from graph-based recall.
type GraphRecallResult struct {
	ID         string // property UUID (table id column = properties->>'id')
	Properties string // raw JSON properties
	TagOverlap int    // number of overlapping tags (0 for key-based recall)
}

// EdgeRelation constants for the relation column of memory_edges.
const (
	EdgeMergedInto     = "MERGED_INTO"
	EdgeExtractedFrom  = "EXTRACTED_FROM"
	EdgeContradicts    = "CONTRADICTS"
	EdgeRelated        = "RELATED"
	EdgeMentionsEntity = "MENTIONS_ENTITY"
)

// entity_edges table stores directed entity-to-entity relationships (triplets).
// Separate from memory_edges to allow efficient entity-graph traversal.
const EnsureEntityEdgesTableSQL = `
CREATE TABLE IF NOT EXISTS entity_edges (
	from_entity_id TEXT NOT NULL,
	predicate      TEXT NOT NULL,
	to_entity_id   TEXT NOT NULL,
	memory_id      TEXT NOT NULL,
	user_name      TEXT NOT NULL,
	valid_at       TEXT,
	invalid_at     TEXT,
	created_at     TEXT,
	PRIMARY KEY (from_entity_id, predicate, to_entity_id, user_name)
);
CREATE INDEX IF NOT EXISTS entity_edges_from_idx ON entity_edges(from_entity_id, user_name);
CREATE INDEX IF NOT EXISTS entity_edges_to_idx ON entity_edges(to_entity_id, user_name)`

// EnsureEdgesTable creates the memory_edges table and index if they do not exist.
// Also adds the valid_at column to existing tables (idempotent via IF NOT EXISTS).
// Called once during Postgres initialization. Non-fatal on error.
func (p *Postgres) EnsureEdgesTable(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS memory_edges (
			from_id    TEXT NOT NULL,
			to_id      TEXT NOT NULL,
			relation   TEXT NOT NULL,
			created_at TEXT,
			valid_at   TEXT,
			invalid_at TEXT,
			PRIMARY KEY (from_id, to_id, relation)
		)`,
		`CREATE INDEX IF NOT EXISTS memory_edges_to_idx ON memory_edges(to_id, relation)`,
		// Migrations: add temporal columns to existing tables.
		`ALTER TABLE memory_edges ADD COLUMN IF NOT EXISTS valid_at TEXT`,
		`ALTER TABLE memory_edges ADD COLUMN IF NOT EXISTS invalid_at TEXT`,
		// Index for filtering active edges (invalid_at IS NULL) efficiently.
		`CREATE INDEX IF NOT EXISTS memory_edges_active_idx ON memory_edges(to_id, relation) WHERE invalid_at IS NULL`,
	}
	for _, stmt := range stmts {
		if _, err := p.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ensure edges table: %w", err)
		}
	}
	return nil
}

// CreateMemoryEdge inserts a directed edge between two memory nodes (idempotent via ON CONFLICT).
// validAt is the ISO-8601 timestamp when this fact became true (from ExtractedFact.ValidAt).
// Pass empty string if unknown — it will be stored as NULL.
// Non-fatal caller pattern: log the error and continue — edges enrich graph recall
// but are not required for core memory operations.
func (p *Postgres) CreateMemoryEdge(ctx context.Context, fromID, toID, relation, createdAt, validAt string) error {
	if fromID == "" || toID == "" || relation == "" {
		return nil
	}
	_, err := p.pool.Exec(ctx, queries.InsertMemoryEdge, fromID, toID, relation, createdAt, validAt)
	if err != nil {
		return fmt.Errorf("create memory edge %s -[%s]-> %s: %w", fromID, relation, toID, err)
	}
	return nil
}

// EnsureEntityEdgesTable creates the entity_edges table and indexes if they do not exist.
// Called once during Postgres initialization. Non-fatal on error.
func (p *Postgres) EnsureEntityEdgesTable(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS entity_edges (
			from_entity_id TEXT NOT NULL,
			predicate      TEXT NOT NULL,
			to_entity_id   TEXT NOT NULL,
			memory_id      TEXT NOT NULL,
			user_name      TEXT NOT NULL,
			valid_at       TEXT,
			invalid_at     TEXT,
			created_at     TEXT,
			PRIMARY KEY (from_entity_id, predicate, to_entity_id, user_name)
		)`,
		`CREATE INDEX IF NOT EXISTS entity_edges_from_idx ON entity_edges(from_entity_id, user_name)`,
		`CREATE INDEX IF NOT EXISTS entity_edges_to_idx ON entity_edges(to_entity_id, user_name)`,
		// Migrations: add temporal columns to existing tables.
		`ALTER TABLE entity_edges ADD COLUMN IF NOT EXISTS invalid_at TEXT`,
		// Partial index for active (non-invalidated) entity edges.
		`CREATE INDEX IF NOT EXISTS entity_edges_active_idx ON entity_edges(from_entity_id, user_name) WHERE invalid_at IS NULL`,
	}
	for _, stmt := range stmts {
		if _, err := p.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ensure entity_edges table: %w", err)
		}
	}
	return nil
}

// UpsertEntityEdge inserts a directed entity-to-entity relationship (triplet) idempotently.
// fromEntityID and toEntityID are normalized entity IDs (from NormalizeEntityID).
// Non-fatal caller pattern: log and continue.
func (p *Postgres) UpsertEntityEdge(ctx context.Context, fromEntityID, predicate, toEntityID, memoryID, userName, validAt, createdAt string) error {
	if fromEntityID == "" || predicate == "" || toEntityID == "" {
		return nil
	}
	const q = `
INSERT INTO entity_edges (from_entity_id, predicate, to_entity_id, memory_id, user_name, valid_at, created_at)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7)
ON CONFLICT (from_entity_id, predicate, to_entity_id, user_name) DO NOTHING`
	_, err := p.pool.Exec(ctx, q, fromEntityID, predicate, toEntityID, memoryID, userName, validAt, createdAt)
	if err != nil {
		return fmt.Errorf("upsert entity edge %s -[%s]-> %s: %w", fromEntityID, predicate, toEntityID, err)
	}
	return nil
}

// InvalidateEdgesByMemoryID sets invalid_at on all memory_edges where from_id = memoryID.
// Called when a memory is deleted or superseded — marks its outgoing edges as no longer active.
// Graphiti pattern: edges are never hard-deleted; invalid_at records when a fact stopped being true.
// invalidAt should be the ISO-8601 timestamp of the superseding fact (or "now" if unknown).
func (p *Postgres) InvalidateEdgesByMemoryID(ctx context.Context, memoryID, invalidAt string) error {
	if memoryID == "" || invalidAt == "" {
		return nil
	}
	const q = `
UPDATE memory_edges
SET invalid_at = $2
WHERE from_id = $1
  AND invalid_at IS NULL`
	_, err := p.pool.Exec(ctx, q, memoryID, invalidAt)
	if err != nil {
		return fmt.Errorf("invalidate edges for memory %s: %w", memoryID, err)
	}
	return nil
}

// InvalidateEntityEdgesByMemoryID sets invalid_at on all entity_edges where memory_id = memoryID.
// Called when the memory that sourced a triplet is deleted or superseded.
// This preserves the historical record while excluding the edge from active recall.
func (p *Postgres) InvalidateEntityEdgesByMemoryID(ctx context.Context, memoryID, invalidAt string) error {
	if memoryID == "" || invalidAt == "" {
		return nil
	}
	const q = `
UPDATE entity_edges
SET invalid_at = $2
WHERE memory_id = $1
  AND invalid_at IS NULL`
	_, err := p.pool.Exec(ctx, q, memoryID, invalidAt)
	if err != nil {
		return fmt.Errorf("invalidate entity edges for memory %s: %w", memoryID, err)
	}
	return nil
}

// GraphRecallByEdge returns memory nodes reachable from seed IDs via directed edges of a given relation.
func (p *Postgres) GraphRecallByEdge(ctx context.Context, seedIDs []string, relation, userName string, limit int) ([]GraphRecallResult, error) {
	if len(seedIDs) == 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.GraphRecallByEdge, graphName)
	rows, err := p.pool.Query(ctx, q, seedIDs, relation, userName, limit)
	if err != nil {
		return nil, fmt.Errorf("graph recall by edge: %w", err)
	}
	defer rows.Close()

	var results []GraphRecallResult
	for rows.Next() {
		var r GraphRecallResult
		if err := rows.Scan(&r.ID, &r.Properties); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GraphRecallByKey finds nodes where properties->>'key' matches any given key.
func (p *Postgres) GraphRecallByKey(ctx context.Context, userName string, memoryTypes []string, keys []string, agentID string, limit int) ([]GraphRecallResult, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.GraphRecallByKey, graphName)
	rows, err := p.pool.Query(ctx, q, userName, memoryTypes, keys, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("graph recall by key: %w", err)
	}
	defer rows.Close()

	var results []GraphRecallResult
	for rows.Next() {
		var r GraphRecallResult
		if err := rows.Scan(&r.ID, &r.Properties); err != nil {
			return nil, fmt.Errorf("graph recall by key scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GraphRecallByTags finds nodes with >= 2 overlapping tags.
func (p *Postgres) GraphRecallByTags(ctx context.Context, userName string, memoryTypes []string, tags []string, agentID string, limit int) ([]GraphRecallResult, error) {
	if len(tags) < 2 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.GraphRecallByTags, graphName)
	rows, err := p.pool.Query(ctx, q, userName, memoryTypes, tags, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("graph recall by tags: %w", err)
	}
	defer rows.Close()

	var results []GraphRecallResult
	for rows.Next() {
		var r GraphRecallResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.TagOverlap); err != nil {
			return nil, fmt.Errorf("graph recall by tags scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GraphBFSTraversal expands a set of seed node IDs up to `depth` hops by following
// working_binding relationships in the properties->>'background' field.
// Returns neighboring nodes not already in the seed set.
// Non-fatal: returns nil on error (caller logs and continues).
func (p *Postgres) GraphBFSTraversal(ctx context.Context, seedIDs []string, userName string, memoryTypes []string, depth, limit int, agentID string) ([]GraphRecallResult, error) {
	if len(seedIDs) == 0 || depth <= 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.GraphBFSTraversal, graphName)
	rows, err := p.pool.Query(ctx, q, seedIDs, userName, memoryTypes, depth, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("graph bfs traversal: %w", err)
	}
	defer rows.Close()

	var results []GraphRecallResult
	for rows.Next() {
		var r GraphRecallResult
		if err := rows.Scan(&r.ID, &r.Properties); err != nil {
			return nil, fmt.Errorf("graph bfs scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// FilterExistingContentHashes returns the subset of hashes that already exist for a user.
// Used to batch-deduplicate before insert: compute textHash for each candidate, then filter.
// Returns a set (map[hash]true) of hashes that are already stored.
func (p *Postgres) FilterExistingContentHashes(ctx context.Context, hashes []string, userName string) (map[string]bool, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.FilterExistingContentHashes, graphName)
	rows, err := p.pool.Query(ctx, q, hashes, userName)
	if err != nil {
		return nil, fmt.Errorf("filter existing hashes: %w", err)
	}
	defer rows.Close()

	existing := make(map[string]bool, len(hashes))
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			continue
		}
		if h != "" {
			existing[h] = true
		}
	}
	return existing, rows.Err()
}
