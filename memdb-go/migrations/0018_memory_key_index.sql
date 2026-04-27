-- Trigram GIN index on properties->>'key' for fast LIKE prefix search.
-- Used by /product/list_memories_by_prefix (Anthropic memory tool view directory).
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS ix_memory_key_trgm
    ON memos_graph."Memory"
    USING GIN ((properties->>'key') gin_trgm_ops);
