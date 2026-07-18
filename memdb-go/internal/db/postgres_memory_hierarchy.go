package db

// postgres_memory_hierarchy.go — D3 tree-reorganizer hierarchy column operations.
// Covers: SetHierarchyLevel (promote/demote a memory between raw/episodic/semantic
// tiers), PromoteClusterChild (atomic edge+hierarchy in one tx),
// ListMemoriesByHierarchyLevel lives in postgres_memory_ltm.go because
// it sits on the same vector-search data path.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// SetHierarchyLevel mutates a Memory node's `hierarchy_level` + `parent_memory_id`
// properties without rewriting the rest of the node (no re-embedding).
//
// Semantics (matches Python tree_text_memory/organize manager.py):
//   - level: 'raw' | 'episodic' | 'semantic'
//   - parentMemoryID: UUID of the parent-tier memory this one was consolidated
//     into. Empty string clears the property (JSON null).
//   - updatedAt: ISO-8601 UTC. Bumps properties.updated_at so D1 decay picks up
//     the promotion as a freshness signal.
//
// The AGE `ag_catalog` layer stores `properties` as agtype; we round-trip through
// jsonb to mutate three keys atomically. No-op if the row doesn't exist (silent
// by design — the caller can lose a race with a concurrent hard-delete and we
// don't want that to abort a whole consolidation cycle).
func (p *Postgres) SetHierarchyLevel(ctx context.Context, memoryID, level, parentMemoryID, updatedAt string) error {
	if memoryID == "" || level == "" {
		return nil
	}
	const q = `
UPDATE %[1]s."Memory"
SET properties = (
        (properties::text::jsonb || jsonb_build_object(
            'hierarchy_level',  $2::text,
            'parent_memory_id', NULLIF($3, '')::text,
            'updated_at',       $4::text
        ))::text
    )::agtype
WHERE properties->>(('id'::text)) = $1`
	_, err := p.pool.Exec(ctx, fmt.Sprintf(q, graphName), memoryID, level, parentMemoryID, updatedAt)
	if err != nil {
		return fmt.Errorf("set hierarchy level %s → %s: %w", memoryID, level, err)
	}
	return nil
}

// PromoteClusterChild atomically creates a CONSOLIDATED_INTO edge and sets the
// child's hierarchy_level + parent_memory_id in a single PG transaction.
// PF-10: previously these were two separate Exec calls — if the edge succeeded
// but the hierarchy update failed (or vice versa), the DB was left in an
// inconsistent state (edge without parent_memory_id, or hierarchy without edge).
// Now both succeed or both roll back.
func (p *Postgres) PromoteClusterChild(ctx context.Context, childID, parentID, relation, childLevel, now string) error {
	if childID == "" || parentID == "" || relation == "" {
		return nil
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("promote cluster child: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // safe to call after Commit

	// 1. Create edge.
	if _, err := tx.Exec(ctx, fmt.Sprintf(queries.InsertMemoryEdge, graphName), childID, parentID, relation, now, ""); err != nil {
		return fmt.Errorf("promote cluster child: edge %s -[%s]-> %s: %w", childID, relation, parentID, err)
	}

	// 2. Set hierarchy level + parent_memory_id.
	const hierarchySQL = `
UPDATE %[1]s."Memory"
SET properties = (
        (properties::text::jsonb || jsonb_build_object(
            'hierarchy_level',  $1::text,
            'parent_memory_id', NULLIF($2, '')::text,
            'updated_at',       $3::text
        ))::text
    )::agtype
WHERE properties->>(('id'::text)) = $4`
	if _, err := tx.Exec(ctx, fmt.Sprintf(hierarchySQL, graphName), childLevel, parentID, now, childID); err != nil {
		return fmt.Errorf("promote cluster child: hierarchy %s → %s: %w", childID, childLevel, err)
	}

	return tx.Commit(ctx)
}
