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
	EdgeMergedInto       = "MERGED_INTO"
	EdgeExtractedFrom    = "EXTRACTED_FROM"
	EdgeContradicts      = "CONTRADICTS"
	EdgeRelated          = "RELATED"
	EdgeMentionsEntity   = "MENTIONS_ENTITY"
	EdgeConsolidatedInto = "CONSOLIDATED_INTO" // D3 hierarchy: raw→episodic, episodic→semantic
	EdgeCauses           = "CAUSES"            // D3 relation detector: causal link
	EdgeSupports         = "SUPPORTS"          // D3 relation detector: evidential link

	// M8 Stream 10 — structural edges emitted at ingest, no LLM required.
	// Intent is to give D2 multi-hop retrieval enough connectivity to traverse
	// without waiting for the (slow, expensive) D3 reorganizer to fire.
	EdgeSameSession        = "SAME_SESSION"         // both memories share session_id
	EdgeTimelineNext       = "TIMELINE_NEXT"        // immediate predecessor by chat_time within session
	EdgeSimilarCosineHigh  = "SIMILAR_COSINE_HIGH"  // cosine similarity in (0.85, dedupThreshold)
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

// CreateMemoryEdgeWithConfidence inserts an edge carrying the relation-detector
// confidence score + rationale (D3). Same ON CONFLICT idempotence as
// CreateMemoryEdge — the first writer wins (later retries no-op).
//
// confidence is clamped silently to [0, 1] so callers cannot corrupt the
// property. rationale is an opaque LLM justification, truncated by the caller
// if necessary (we do not police length here).
func (p *Postgres) CreateMemoryEdgeWithConfidence(ctx context.Context, fromID, toID, relation, createdAt, validAt string, confidence float64, rationale string) error {
	if fromID == "" || toID == "" || relation == "" {
		return nil
	}
	if confidence < 0 {
		confidence = 0
	} else if confidence > 1 {
		confidence = 1
	}
	_, err := p.pool.Exec(ctx, queries.InsertMemoryEdgeWithConfidence, fromID, toID, relation, createdAt, validAt, confidence, rationale)
	if err != nil {
		return fmt.Errorf("create memory edge w/confidence %s -[%s]-> %s: %w", fromID, relation, toID, err)
	}
	return nil
}

// InsertTreeConsolidationEvent appends a row to memos_graph.tree_consolidation_log.
// Used by TreeManager (D3) to preserve the audit trail of every promotion
// (raw → episodic, episodic → semantic). tier is 'episodic' or 'semantic'.
// childIDs must be the full list of UUIDs that merged into parentID.
// llmModel / promptSHA may be empty for auto-merge (no LLM call).
func (p *Postgres) InsertTreeConsolidationEvent(ctx context.Context, eventID, cubeID, parentID string, childIDs []string, tier, llmModel, promptSHA, createdAt string) error {
	if eventID == "" || cubeID == "" || parentID == "" || len(childIDs) == 0 || tier == "" {
		return nil
	}
	const q = `
INSERT INTO memos_graph.tree_consolidation_log
    (event_id, cube_id, parent_id, child_ids, tier, llm_model, prompt_sha, created_at)
VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8)`
	_, err := p.pool.Exec(ctx, q, eventID, cubeID, parentID, childIDs, tier, llmModel, promptSHA, createdAt)
	if err != nil {
		return fmt.Errorf("insert tree consolidation event %s: %w", eventID, err)
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
