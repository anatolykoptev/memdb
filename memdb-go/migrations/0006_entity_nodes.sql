-- MemDB migration 0006: entity_nodes table + HNSW embedding index
-- Date: 2026-04-23
-- Ports EnsureEntityNodesTable from internal/db/postgres_entity.go into versioned runner.
-- Stores entities extracted from memories (per-user scoped) with halfvec(1024) embeddings
-- used by entity-alias resolution (UpsertEntityNodeWithEmbedding).
-- Idempotent: IF NOT EXISTS on all DDL; ADD COLUMN IF NOT EXISTS on ALTER.
--
-- HNSW halfvec_cosine_ops index preserved exactly (do NOT convert to ivfflat).

CREATE TABLE IF NOT EXISTS entity_nodes (
    id          TEXT NOT NULL,
    user_name   TEXT NOT NULL,
    name        TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    created_at  TEXT,
    updated_at  TEXT,
    embedding   halfvec(1024),
    PRIMARY KEY (id, user_name)
);

CREATE INDEX IF NOT EXISTS entity_nodes_user_idx
    ON entity_nodes(user_name);

CREATE INDEX IF NOT EXISTS entity_nodes_name_idx
    ON entity_nodes(user_name, id);

-- Migration: add embedding column to pre-existing tables.
ALTER TABLE entity_nodes ADD COLUMN IF NOT EXISTS embedding halfvec(1024);

-- HNSW index for fast cosine similarity lookup during identity resolution.
CREATE INDEX IF NOT EXISTS entity_nodes_emb_idx
    ON entity_nodes USING hnsw (embedding halfvec_cosine_ops);
