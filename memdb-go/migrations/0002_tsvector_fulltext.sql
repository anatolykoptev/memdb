-- MemDB migration 0002: fulltext search column (properties_tsvector_zh)
-- Date: 2026-04-23
-- Ports the tsvector bootstrap from src/memdb/graph_dbs/polardb/schema.py.
-- Idempotent: IF NOT EXISTS / CREATE OR REPLACE / DROP TRIGGER IF EXISTS.
-- NOTE: memos_graph."Memory".properties is agtype (AGE vertex), not plain JSONB,
-- so we cast via ::text::jsonb before ->> 'memory'.

ALTER TABLE memos_graph."Memory"
    ADD COLUMN IF NOT EXISTS properties_tsvector_zh tsvector;

CREATE OR REPLACE FUNCTION memos_graph.update_tsvector_zh()
RETURNS trigger AS $$
BEGIN
    NEW.properties_tsvector_zh :=
        to_tsvector('simple', COALESCE(
            (NEW.properties::text::jsonb)->>'memory',
            ''
        ));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_update_tsvector_zh ON memos_graph."Memory";
CREATE TRIGGER trg_update_tsvector_zh
    BEFORE INSERT OR UPDATE ON memos_graph."Memory"
    FOR EACH ROW EXECUTE FUNCTION memos_graph.update_tsvector_zh();

CREATE INDEX IF NOT EXISTS idx_memory_tsvector_zh
    ON memos_graph."Memory" USING GIN (properties_tsvector_zh);

-- Backfill for rows added before trigger existed (NO-OP on fresh DB).
UPDATE memos_graph."Memory"
SET properties_tsvector_zh = to_tsvector('simple', COALESCE(
    (properties::text::jsonb)->>'memory',
    ''
))
WHERE properties_tsvector_zh IS NULL;
