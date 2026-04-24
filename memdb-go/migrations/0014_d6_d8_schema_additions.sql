-- MemDB migration 0014: Phase D / D6+D8 extractor schema additions (audit only)
-- Date: 2026-04-23
--
-- No column changes — new fields live inside properties::agtype JSON:
--   raw_text            : verbatim original from conversation (D6 audit trail)
--   resolved_text       : pronoun+temporal-resolved form — primary retrieval text
--                         (stored under "memory" key; resolved_text is the LLM
--                         output field name, the DB key stays "memory")
--   preference_category : enum of 22 MemOS-taxonomy keys, PreferenceMemory only (D8)
--
-- Backfill: stamp explicit null for preference_category on existing
-- PreferenceMemory rows so downstream code can distinguish "not categorised
-- yet" (null) from "field was never written" (missing key). Idempotent via
-- the NOT (props ? 'preference_category') guard.

UPDATE memos_graph."Memory"
SET properties = (
    (properties::text::jsonb)
    || jsonb_build_object('preference_category', (properties::text::jsonb)->>'preference_category')
)::text::agtype
WHERE (properties::text::jsonb)->>'memory_type' = 'PreferenceMemory'
  AND NOT ((properties::text::jsonb) ? 'preference_category');
