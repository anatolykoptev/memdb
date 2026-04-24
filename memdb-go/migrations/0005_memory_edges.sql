-- MemDB migration 0005: memory_edges table
-- Date: 2026-04-23
-- Ports EnsureEdgesTable from internal/db/postgres_graph_edges.go into versioned runner.
-- Directed memory-to-memory edges (MERGED_INTO, EXTRACTED_FROM, CONTRADICTS,
-- RELATED, MENTIONS_ENTITY) with bi-temporal columns (valid_at, invalid_at).
-- Idempotent: IF NOT EXISTS on all DDL; ADD COLUMN IF NOT EXISTS on ALTER.
--
-- Schema note: table name is unqualified — lands in the first writable schema
-- in search_path (ag_catalog with runner's `ag_catalog, memos_graph, "$user", public`).
-- This matches prod state installed by the previous Ensure* runs.

CREATE TABLE IF NOT EXISTS memory_edges (
    from_id    TEXT NOT NULL,
    to_id      TEXT NOT NULL,
    relation   TEXT NOT NULL,
    created_at TEXT,
    valid_at   TEXT,
    invalid_at TEXT,
    PRIMARY KEY (from_id, to_id, relation)
);

CREATE INDEX IF NOT EXISTS memory_edges_to_idx
    ON memory_edges(to_id, relation);

ALTER TABLE memory_edges ADD COLUMN IF NOT EXISTS valid_at TEXT;
ALTER TABLE memory_edges ADD COLUMN IF NOT EXISTS invalid_at TEXT;

CREATE INDEX IF NOT EXISTS memory_edges_active_idx
    ON memory_edges(to_id, relation) WHERE invalid_at IS NULL;
