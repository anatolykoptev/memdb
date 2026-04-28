-- 0022_atomic_facts.sql — M11 F8: atomic per-fact extraction (mem0 ADDITIVE_PROMPT port).
--
-- F8 introduces a `kind` discriminator on memos_graph."Memory" rows:
--   * paragraph_legacy — pre-F8 condensed paragraph rows (default for all existing rows)
--   * atomic_fact      — post-F8 mem0-style 15–80-word self-contained fact rows
--
-- Both kinds coexist in the same cube so we can A/B and fall back at search time.
-- Legacy rows are NEVER deleted by F8 — they remain a fallback for the env-off
-- path and for cubes that have not been re-extracted yet.
--
-- AGE caveat
-- ----------
-- properties is agtype (Apache AGE), NOT jsonb. agtype's `->>` returns agtype,
-- and the `?` existence operator does not exist on agtype. We route through
-- `properties::text::jsonb` — the same pattern used by queries_search_vector.go.
--
-- Storage strategy
-- ----------------
-- An earlier draft of this migration added `kind` as a STORED GENERATED column,
-- but: (a) Go code never reads the column directly — kind is consumed via
-- `properties::text::jsonb->>'kind'` everywhere, and (b) ALTER TABLE …
-- ADD COLUMN GENERATED requires a full table rewrite that exhausts shared
-- memory on cubes >5k rows. We instead create three EXPRESSION INDEXES that
-- give the planner the same lookup speed without touching the row layout:
--
--   1. idx_memory_kind          — btree on (jsonb->>'kind') for "give me all
--                                 atomic_fact rows in this cube"
--   2. idx_memory_linked_ids    — GIN on (jsonb->'linked_memory_ids') for the
--                                 F12 `?|` 1-hop expansion
--   3. idx_memory_attributed_to — partial btree on (jsonb->>'attributed_to') for
--                                 multi-speaker filtering
--
-- Rollback:
--   DROP INDEX IF EXISTS memos_graph.idx_memory_kind;
--   DROP INDEX IF EXISTS memos_graph.idx_memory_linked_ids;
--   DROP INDEX IF EXISTS memos_graph.idx_memory_attributed_to;

-- Fast filter for kind='atomic_fact' vs kind='paragraph_legacy' at search time.
-- Partial: skip rows that don't carry the property (legacy paragraph rows).
CREATE INDEX IF NOT EXISTS idx_memory_kind
    ON memos_graph."Memory" (((properties::text::jsonb)->>'kind'))
    WHERE (properties::text::jsonb) ? 'kind';

-- linked_memory_ids: ["uuid-1", "uuid-2", ...] — soft graph edges from
-- extract step. GIN supports containment (`@>`) and existence (`?|`).
CREATE INDEX IF NOT EXISTS idx_memory_linked_ids
    ON memos_graph."Memory"
    USING GIN ((properties::text::jsonb -> 'linked_memory_ids'));

-- attributed_to: speaker name (when multi-speaker conversation). Partial
-- index — only indexes rows that have the property to keep size minimal.
CREATE INDEX IF NOT EXISTS idx_memory_attributed_to
    ON memos_graph."Memory" (((properties::text::jsonb)->>'attributed_to'))
    WHERE (properties::text::jsonb) ? 'attributed_to';
