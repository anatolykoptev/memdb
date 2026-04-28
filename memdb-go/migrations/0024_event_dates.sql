-- 0024_event_dates.sql — M11 F7: temporal index on atomic-fact event dates.
--
-- Atomic facts (F8) emit an `event_dates` JSON array on properties — a list of
-- ISO-8601 (YYYY-MM-DD) date strings the fact references, resolved against
-- observation_date by the LLM extractor (see internal/llm/atomic_extractor.go).
--
-- F7 adds:
--   * GIN index on (properties->'event_dates') with jsonb_path_ops — fastest
--     containment ops (@>, ?, ?|, ?&) for the temporal augmentation query.
--   * Partial index — only rows that actually carry the property are indexed,
--     keeping the structure small (atomic facts are a fraction of all rows on
--     legacy cubes that haven't been re-extracted yet).
--
-- Rollback:
--   DROP INDEX IF EXISTS memos_graph.idx_memory_event_dates;
--
-- Idempotent: CREATE INDEX IF NOT EXISTS.

CREATE INDEX IF NOT EXISTS idx_memory_event_dates
    ON memos_graph."Memory"
    USING GIN ((properties -> 'event_dates') jsonb_path_ops)
    WHERE properties ? 'event_dates';
