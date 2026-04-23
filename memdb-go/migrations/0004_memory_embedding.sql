-- MemDB migration 0004: Memory.embedding column + HNSW halfvec index
-- Date: 2026-04-23
-- Prod-accurate port: vector(1024) column + HNSW halfvec_cosine_ops index.
-- Supersedes Python schema.py's obsolete ivfflat/JSONB variants.
-- Idempotent: IF NOT EXISTS on both ALTER and CREATE INDEX.

ALTER TABLE memos_graph."Memory"
    ADD COLUMN IF NOT EXISTS embedding vector(1024);

CREATE INDEX IF NOT EXISTS idx_memory_embedding
    ON memos_graph."Memory"
    USING hnsw ((embedding::halfvec(1024)) halfvec_cosine_ops);
