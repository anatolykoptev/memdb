-- Trigram GIN index on properties->>'key' for fast LIKE prefix search.
-- Used by /product/list_memories_by_prefix (Anthropic memory tool view directory).
-- NOTE: properties is agtype (Apache AGE), not jsonb. AGE requires the key literal
-- to be explicitly cast to text, and the result needs ::text to land at the
-- gin_trgm_ops input type. Pattern matches queries_search_vector.go convention.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS ix_memory_key_trgm
    ON memos_graph."Memory"
    USING GIN (((properties->>('key'::text))::text) gin_trgm_ops);
