-- MemDB migration 0028: extraction_state on Memory.properties.
-- Date: 2026-04-29
--
-- Adds the `extraction_state` property key to every Memory row. Default is
-- 'extracted' — this migration backfills existing rows. Future code that wants
-- to defer LLM-driven enrichment can flip selected rows to 'pending'; a partial
-- index keeps the SELECT cheap, but no scheduler loop reads from it today
-- (cancelled — see CLAUDE.md `Add-mode contract`).
--
-- The key is written by the Go handler at insert time via
-- add_props.go::buildMemoryProperties. State constants (pending | extracting |
-- extracted | failed) are defined as extractionState* in add_props.go.
--
-- Idempotent: IF NOT EXISTS on the index, UPDATE skipped when key already set.
--
-- Rollback:
--   DROP INDEX IF EXISTS memos_graph.idx_memory_extraction_pending;
--   UPDATE memos_graph."Memory" SET properties = (properties::text::jsonb - 'extraction_state')::text::agtype;

UPDATE memos_graph."Memory"
   SET properties = (
        (properties::text::jsonb)
        || jsonb_build_object('extraction_state', 'extracted')
       )::text::agtype
 WHERE NOT ((properties::text::jsonb) ? 'extraction_state');

CREATE INDEX IF NOT EXISTS idx_memory_extraction_pending
    ON memos_graph."Memory" ((((properties::text::jsonb) ->> 'extraction_state')))
 WHERE ((properties::text::jsonb) ->> 'extraction_state') = 'pending';
