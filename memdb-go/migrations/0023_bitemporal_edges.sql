-- MemDB migration 0023: bi-temporal edges (M11 F11)
-- Date: 2026-04-27
-- Adds supporting indexes for the F11 bi-temporal edge invalidator. The
-- valid_at / invalid_at columns themselves were introduced by 0005 (memory_edges)
-- and 0007 (entity_edges) — both as TEXT to match the existing helper layer.
-- This migration is purely additive: idempotent indexes, no column changes.
--
-- New indexes:
--   1. entity_edges_active_idx — partial index on currently-valid edges, mirrors
--      memory_edges_active_idx (added by 0005). Speeds up the GetMemoriesByEntityIDs
--      / entity-graph filters that already include `invalid_at IS NULL`.
--   2. entity_edges_subject_predicate_idx — supports the F11 judge's
--      "find prior edges with same (from_entity_id, predicate) for this user"
--      lookup it issues per newly-extracted edge. Conditional on invalid_at
--      IS NULL so the planner can ignore historical rows.
--
-- Note: memory_edges_from_relation_idx was dropped — the memory_edges F11
-- helpers (FetchActiveMemoryEdgesBySubject, InvalidateMemoryEdgesByTriples)
-- were dead code. memory_edges bi-temporal coverage is deferred to M12.
--
-- All indexes are CREATE INDEX IF NOT EXISTS; safe to run repeatedly.

CREATE INDEX IF NOT EXISTS entity_edges_active_idx
    ON entity_edges(from_entity_id, predicate)
    WHERE invalid_at IS NULL;

CREATE INDEX IF NOT EXISTS entity_edges_subject_predicate_idx
    ON entity_edges(user_name, from_entity_id, predicate)
    WHERE invalid_at IS NULL;
