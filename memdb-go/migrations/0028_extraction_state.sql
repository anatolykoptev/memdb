-- MemDB migration 0028: extraction_state on Memory.properties (uniform pipeline).
-- Date: 2026-04-29
--
-- Adds three property keys read by the new extraction_filler scheduler loop:
--   extraction_state           : 'pending'|'extracting'|'extracted'|'failed'
--   extraction_attempted_at    : ISO-8601 timestamp of the last filler attempt
--   extraction_completed_at    : ISO-8601 timestamp of the successful fill
--
-- Memory.properties is agtype (Apache AGE), so the keys are written by the Go
-- handler when the row is inserted (see add_props.go::BuildMemoryProperties).
-- This SQL only:
--   1) backfills every existing row to extraction_state='extracted' (legacy
--      assumption — see plan Task 1 for rationale; opt-in re-extraction lives
--      in Task 5).
--   2) creates a partial index on extraction_state='pending' so the filler
--      loop's batch SELECT does not table-scan in steady state.
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
