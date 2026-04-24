-- MemDB migration 0011: Memory.access_count property for D1 importance scoring
-- Date: 2026-04-24
-- Adds a property-level counter incremented on each retrieval hit. Paired
-- with IncrRetrievalCount (queries_graph.go) — that query is updated in the
-- same PR to bump access_count alongside retrieval_count.
-- Idempotent: WHERE clause skips rows that already have the field.

-- AGE vertex Memory stores everything in properties::agtype. We add the
-- access_count field inside the JSON blob (no table-level column).

-- Backfill existing Memory rows with properties.access_count = 0 (if missing).
UPDATE memos_graph."Memory"
SET properties = (
    (properties::text::jsonb)
    || jsonb_build_object('access_count', 0)
)::text::agtype
WHERE (properties::text::jsonb)->>'access_count' IS NULL;
