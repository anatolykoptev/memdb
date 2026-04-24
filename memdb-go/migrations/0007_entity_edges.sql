-- MemDB migration 0007: entity_edges table
-- Date: 2026-04-23
-- Ports EnsureEntityEdgesTable from internal/db/postgres_graph_edges.go into versioned runner.
-- Directed entity-to-entity relationship triplets (from_entity_id, predicate,
-- to_entity_id), scoped per user, with bi-temporal columns (valid_at, invalid_at).
-- Separate from memory_edges to allow efficient entity-graph traversal.
-- Idempotent: IF NOT EXISTS on all DDL; ADD COLUMN IF NOT EXISTS on ALTER.

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

CREATE INDEX IF NOT EXISTS entity_edges_from_idx
    ON entity_edges(from_entity_id, user_name);

CREATE INDEX IF NOT EXISTS entity_edges_to_idx
    ON entity_edges(to_entity_id, user_name);

ALTER TABLE entity_edges ADD COLUMN IF NOT EXISTS invalid_at TEXT;

CREATE INDEX IF NOT EXISTS entity_edges_active_idx
    ON entity_edges(from_entity_id, user_name) WHERE invalid_at IS NULL;
