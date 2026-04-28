-- 0024_event_dates.sql — M11 F7: temporal index on atomic-fact event dates.
--
-- Atomic facts (F8) emit an `event_dates` JSON array on properties — a list of
-- ISO-8601 (YYYY-MM-DD) date strings the fact references, resolved against
-- observation_date by the LLM extractor (see internal/llm/atomic_extractor.go).
--
-- AGE caveat
-- ----------
-- properties is agtype (Apache AGE), not jsonb. agtype's `->` returns agtype,
-- and the `?` existence operator does not exist on agtype. We route through
-- `properties::text::jsonb` — the same pattern used in queries_search_vector.go
-- and migration 0022.
--
-- F7 adds:
--   * GIN index on (properties::text::jsonb -> 'event_dates') with jsonb_path_ops
--     — fastest containment ops (@>, ?, ?|, ?&) for the temporal augmentation query.
--   * Partial index — only rows that actually carry the property are indexed,
--     keeping the structure small (atomic facts are a fraction of all rows on
--     legacy cubes that haven't been re-extracted yet).
--
-- LIMITATION: this GIN partial index helps Postgres skip rows without the
-- `event_dates` property, but it does NOT accelerate range filtering inside
-- the unnested array (jsonb_path_ops supports containment, not range). The
-- F7 search query still does a per-row jsonb_array_elements_text scan inside
-- the EXISTS subquery; benchmark before claiming sub-30ms p95 on cubes
-- larger than ~50k atomic facts.
--
-- Rollback:
--   DROP INDEX IF EXISTS memos_graph.idx_memory_event_dates;
--
-- Idempotent: CREATE INDEX IF NOT EXISTS.

CREATE INDEX IF NOT EXISTS idx_memory_event_dates
    ON memos_graph."Memory"
    USING GIN (((properties::text::jsonb) -> 'event_dates') jsonb_path_ops)
    WHERE (properties::text::jsonb) ? 'event_dates';
