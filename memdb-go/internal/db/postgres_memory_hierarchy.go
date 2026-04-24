package db

// postgres_memory_hierarchy.go — D3 tree-reorganizer hierarchy column operations.
// Covers: SetHierarchyLevel (promote/demote a memory between raw/episodic/semantic
// tiers), ListMemoriesByHierarchyLevel lives in postgres_memory_ltm.go because
// it sits on the same vector-search data path.

import (
	"context"
	"fmt"
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
