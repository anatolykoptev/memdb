-- 0022_atomic_facts.sql — M11 F8: atomic per-fact extraction (mem0 ADDITIVE_PROMPT port).
--
-- Adds a `kind` column on memos_graph."Memory" to distinguish:
--   * paragraph_legacy — pre-F8 condensed paragraph rows (default for all existing rows)
--   * atomic_fact      — post-F8 mem0-style 15–80-word self-contained fact rows
--
-- Both kinds coexist in the same cube so we can A/B and fall back at search time.
-- The legacy rows are NEVER deleted by F8 — they remain a fallback for the
-- env-off path and for cubes that have not been re-extracted yet.
--
-- Column population strategy
-- --------------------------
-- The existing INSERT path writes only (properties, embedding) — adding a
-- third column would force every callsite to thread `kind` through. We avoid
-- that by making `kind` a STORED GENERATED column derived from
-- properties->>'kind'. New atomic-fact rows set properties.kind='atomic_fact'
-- in their JSONB; rows without that property fall back to 'paragraph_legacy'
-- via COALESCE. This deviation from the original F8 spec preserves the
-- byte-identical legacy insert path while still giving us a queryable column
-- + index for fast filtering.
--
-- Rollback
-- --------
--   ALTER TABLE memos_graph."Memory" DROP COLUMN IF EXISTS kind;
--   DROP INDEX IF EXISTS memos_graph.idx_memory_kind;
--   DROP INDEX IF EXISTS memos_graph.idx_memory_linked_ids;
--   DROP INDEX IF EXISTS memos_graph.idx_memory_attributed_to;

-- kind: STORED generated column derived from properties->>'kind'.
-- Defaults to 'paragraph_legacy' for any row that does not set the property.
ALTER TABLE memos_graph."Memory"
    ADD COLUMN IF NOT EXISTS kind TEXT
    GENERATED ALWAYS AS (
        COALESCE(NULLIF((properties->>'kind'), ''), 'paragraph_legacy')
    ) STORED;

-- Fast filter for kind='atomic_fact' vs kind='paragraph_legacy' at search time.
CREATE INDEX IF NOT EXISTS idx_memory_kind
    ON memos_graph."Memory" (kind);

-- linked_memory_ids: ["uuid-1", "uuid-2", ...] — soft graph edges from
-- extract step. GIN allows containment queries (jsonb @> '"uuid"').
CREATE INDEX IF NOT EXISTS idx_memory_linked_ids
    ON memos_graph."Memory"
    USING GIN ((properties -> 'linked_memory_ids'));

-- attributed_to: speaker name (when multi-speaker conversation). Partial
-- index — only indexes rows that have the property to keep size minimal.
CREATE INDEX IF NOT EXISTS idx_memory_attributed_to
    ON memos_graph."Memory" ((properties ->> 'attributed_to'))
    WHERE properties ? 'attributed_to';
