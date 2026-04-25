-- MemDB migration 0015: user_profiles table for memobase profile port (M10 Stream 1)
-- Date: 2026-04-25
--
-- Adds memos_graph.user_profiles — a structured fact store for per-user topic/sub_topic memos.
-- Soft-delete via expired_at: active rows have expired_at IS NULL.
-- BulkUpsert logic (expire-then-insert) is handled in Go (postgres_profiles.go).
-- tsvector trigger keeps memo_tsv current for full-text search.

CREATE TABLE IF NOT EXISTS memos_graph.user_profiles (
    id           BIGSERIAL    PRIMARY KEY,
    user_id      TEXT         NOT NULL,
    topic        TEXT         NOT NULL,
    sub_topic    TEXT         NOT NULL,
    memo         TEXT         NOT NULL,
    confidence   REAL         NOT NULL DEFAULT 1.0 CHECK (confidence BETWEEN 0 AND 1),
    valid_at     TIMESTAMPTZ,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    expired_at   TIMESTAMPTZ,
    memo_tsv     TSVECTOR
);

-- Unique: one active entry per (user_id, topic, sub_topic).
-- Expired rows are excluded (allowing historical rows to coexist).
CREATE UNIQUE INDEX IF NOT EXISTS ux_user_profiles_user_topic_sub
    ON memos_graph.user_profiles (user_id, topic, sub_topic)
    WHERE expired_at IS NULL;

-- Lookup by user+topic (active only).
CREATE INDEX IF NOT EXISTS ix_user_profiles_user_topic
    ON memos_graph.user_profiles (user_id, topic)
    WHERE expired_at IS NULL;

-- GIN index for full-text search on memo_tsv.
CREATE INDEX IF NOT EXISTS gin_user_profiles_memo_tsv
    ON memos_graph.user_profiles USING GIN (memo_tsv);

-- Trigger function: keep memo_tsv in sync with memo on insert/update.
CREATE OR REPLACE FUNCTION memos_graph.user_profiles_tsv_update()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.memo_tsv := to_tsvector('simple', COALESCE(NEW.memo, ''));
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_user_profiles_tsv ON memos_graph.user_profiles;
CREATE TRIGGER trg_user_profiles_tsv
    BEFORE INSERT OR UPDATE OF memo
    ON memos_graph.user_profiles
    FOR EACH ROW EXECUTE PROCEDURE memos_graph.user_profiles_tsv_update();
