-- MemDB migration 0017: add cube_id to user_profiles for cube isolation (security audit C1).
-- Date: 2026-04-25
--
-- Background:
--   memos_graph.user_profiles (added in 0015) had no cube_id column. This let
--   profile rows extracted in cube=A leak into the chat system prompt of a
--   later request scoped to cube=B for the same user_id. Every other M10
--   stream honours cube isolation; user_profiles is the lone exception.
--   See PR / security audit finding C1 for the cross-tenant repro.
--
-- Backfill policy:
--   Existing rows are left with cube_id = NULL. Production has only test data
--   at this stage of M10 (no benchmark-scale profile rows exist yet). NULL is
--   treated as "global / pre-cube" by the application: never returned by the
--   cube-scoped getter (GetProfilesByUserCube). A follow-up migration (0018)
--   will flip the column to NOT NULL once the legacy NULL rows have been
--   reaped or backfilled.
--
-- Index reshape:
--   The pre-existing unique index ux_user_profiles_user_topic_sub did not
--   include cube_id, so two cubes could not host the same (topic, sub_topic)
--   tuple for the same user. We drop it and recreate the uniqueness scope as
--   (cube_id, user_id, topic, sub_topic). The lookup index is replaced in
--   parallel.

ALTER TABLE memos_graph.user_profiles
    ADD COLUMN IF NOT EXISTS cube_id TEXT;

-- Replace the cube-unaware unique index with a cube-scoped one.
DROP INDEX IF EXISTS memos_graph.ux_user_profiles_user_topic_sub;

CREATE UNIQUE INDEX IF NOT EXISTS ux_user_profiles_cube_user_topic_sub
    ON memos_graph.user_profiles (cube_id, user_id, topic, sub_topic)
    WHERE expired_at IS NULL;

-- Replace the cube-unaware lookup index with a cube-scoped one.
DROP INDEX IF EXISTS memos_graph.ix_user_profiles_user_topic;

CREATE INDEX IF NOT EXISTS ix_user_profiles_cube_user_topic
    ON memos_graph.user_profiles (cube_id, user_id, topic)
    WHERE expired_at IS NULL;
