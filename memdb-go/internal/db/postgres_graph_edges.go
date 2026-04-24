package db

// postgres_graph_edges.go — memory_edges and entity_edges table operations.
// Covers: EdgeRelation constants, CreateMemoryEdge, UpsertEntityEdge,
// InvalidateEdgesByMemoryID, InvalidateEntityEdgesByMemoryID,
// FilterExistingContentHashes.
//
// Table creation moved to migrations 0005_memory_edges.sql and
// 0007_entity_edges.sql (applied by RunMigrations at startup).

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// EdgeRelation constants for the relation column of memory_edges.
const (
	EdgeMergedInto     = "MERGED_INTO"
	EdgeExtractedFrom  = "EXTRACTED_FROM"
	EdgeContradicts    = "CONTRADICTS"
	EdgeRelated        = "RELATED"
	EdgeMentionsEntity = "MENTIONS_ENTITY"
)

// EnsureEntityEdgesTableSQL is the reference DDL for entity_edges.
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

// CreateMemoryEdge inserts a directed edge between two memory nodes (idempotent via ON CONFLICT).
// validAt is the ISO-8601 timestamp when this fact became true. Pass empty string for NULL.
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

// UpsertEntityEdge inserts a directed entity-to-entity relationship (triplet) idempotently.
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

// FilterExistingContentHashes returns the subset of hashes that already exist for a user.
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
