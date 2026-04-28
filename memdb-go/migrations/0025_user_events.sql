-- MemDB migration 0024: user_events table (M11 F3 — Memobase event extraction).
-- Date: 2026-04-28
--
-- Stores per-user event summaries with date anchors and free-form tags. Rows
-- are produced by the EventExtractor (internal/llm/event_extractor.go), one
-- of the seven background extractors fanned out by triggerBackgroundExtractors.
-- At search time stageInjectEvents pulls top-N relevant events for the query
-- (date-window | tag-overlap | cosine) and injects them as text candidates
-- before rerank.
--
-- Cube isolation mirrors user_profiles (migration 0017): every row carries
-- (cube_id, user_id) so events extracted in cube A never leak into cube B.
--
-- Embedding column is vector(1024) with HNSW halfvec index — same shape as
-- 0004_memory_embedding.sql and 0006_entity_nodes.sql (HNSW preferred over
-- ivfflat for better recall on the multilingual-e5-large embedder).
--
-- Idempotent: IF NOT EXISTS on table + indexes.
--
-- Rollback:
--   DROP TABLE IF EXISTS memos_graph.user_events CASCADE;

CREATE TABLE IF NOT EXISTS memos_graph.user_events (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    cube_id      TEXT         NOT NULL,
    user_id      TEXT         NOT NULL,
    event_text   TEXT         NOT NULL,
    event_date   DATE,
    tags         TEXT[]       NOT NULL DEFAULT '{}',
    embedding    vector(1024),
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Tenant scoping: most queries filter by (cube_id, user_id) first.
CREATE INDEX IF NOT EXISTS ix_user_events_cube_user
    ON memos_graph.user_events (cube_id, user_id);

-- Tag-overlap lookup (SearchByTag): GIN on text[] for `tags && $1`.
CREATE INDEX IF NOT EXISTS gin_user_events_tags
    ON memos_graph.user_events USING GIN (tags);

-- Date-window lookup (SearchByDate): btree on event_date, partial to skip
-- rows where the LLM could not anchor a date.
CREATE INDEX IF NOT EXISTS ix_user_events_event_date
    ON memos_graph.user_events (event_date)
    WHERE event_date IS NOT NULL;

-- Vector cosine lookup (SearchByCosine): HNSW halfvec_cosine_ops mirrors
-- the existing Memory.embedding index strategy (0004). Halfvec cuts the
-- index size in half with negligible recall loss on 1024-dim e5-large.
CREATE INDEX IF NOT EXISTS hnsw_user_events_embedding
    ON memos_graph.user_events
    USING hnsw ((embedding::halfvec(1024)) halfvec_cosine_ops);
