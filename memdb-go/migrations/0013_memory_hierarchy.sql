-- MemDB migration 0013: Memory hierarchy fields for D3 tree reorganizer
-- Date: 2026-04-25
-- Ports Python tree_text_memory/organize field additions.
-- Fields live inside properties::agtype as JSON keys:
--   hierarchy_level: 'raw' | 'episodic' | 'semantic' (default 'raw')
--   parent_memory_id: UUID of parent tier's memory (null for raw / orphan episodic)
--   consolidation_source_ids: array of child UUIDs that merged into this one
--     (populated by TreeManager when promoting a cluster)
-- Idempotent backfill: skips rows that already have hierarchy_level set.
--
-- Also adds two new columns on memory_edges (D3 relation-detector):
--   confidence — float score in [0,1] attached by the relation detector
--   rationale  — free-text justification from the LLM (diagnostic only)
-- Both are NULLable so existing rows remain valid and the D2 graph
-- traversal code (which does not read these columns) is unaffected.
-- Idempotent via ADD COLUMN IF NOT EXISTS.
--
-- Finally creates tree_consolidation_log — the audit trail for every
-- consolidation event (porting Python history_manager.py semantics).
-- Stores which children merged into which parent and which model did it.
-- Idempotent via CREATE TABLE IF NOT EXISTS.

-- 1. Backfill hierarchy_level on existing Memory nodes.
UPDATE memos_graph."Memory"
SET properties = (
    (properties::text::jsonb)
    || jsonb_build_object(
        'hierarchy_level',  COALESCE((properties::text::jsonb)->>'hierarchy_level', 'raw'),
        'parent_memory_id', (properties::text::jsonb)->>'parent_memory_id'
    )
)::text::agtype
WHERE (properties::text::jsonb)->>'hierarchy_level' IS NULL;

-- 2. Add relation detector columns on memory_edges.
ALTER TABLE memos_graph.memory_edges ADD COLUMN IF NOT EXISTS confidence DOUBLE PRECISION;
ALTER TABLE memos_graph.memory_edges ADD COLUMN IF NOT EXISTS rationale TEXT;

-- 3. Consolidation audit log (history_manager.py port).
CREATE TABLE IF NOT EXISTS memos_graph.tree_consolidation_log (
    event_id    UUID PRIMARY KEY,
    cube_id     TEXT NOT NULL,
    parent_id   TEXT NOT NULL,           -- UUID of the new (episodic/semantic) memory
    child_ids   TEXT[] NOT NULL,         -- UUIDs that merged into parent_id
    tier        TEXT NOT NULL,           -- 'episodic' | 'semantic'
    llm_model   TEXT,                    -- model id from llm.Client, null for auto-merge
    prompt_sha  TEXT,                    -- sha256(prompt) for reproducibility
    created_at  TEXT NOT NULL            -- ISO-8601 UTC
);

CREATE INDEX IF NOT EXISTS tree_consolidation_log_cube_idx
    ON memos_graph.tree_consolidation_log(cube_id, created_at DESC);
CREATE INDEX IF NOT EXISTS tree_consolidation_log_parent_idx
    ON memos_graph.tree_consolidation_log(parent_id);
