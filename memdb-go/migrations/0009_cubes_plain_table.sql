-- MemDB migration 0009: fix cubes table shape
-- Date: 2026-04-24
-- Prod has memos_graph.cubes as an AGE vertex label (columns: id graphid, properties agtype).
-- Migration 0001 (baselined but never executed against prod) expected a plain table with
-- named columns. Go code relies on the plain-table shape (cube_id, cube_name, owner_id, ...)
-- — every EnsureCubeExists / UpsertCube / ListCubes fails against the vertex shape.
-- This migration drops the AGE label and recreates the plain table per 0001's design.
-- Safe: cubes has 0 rows on prod (confirmed via SELECT count(*)). Idempotent via guards.

DO $$
DECLARE
    is_age_vertex boolean;
    has_cube_id boolean;
BEGIN
    -- Detect current shape.
    SELECT EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class p ON p.oid = i.inhparent
        JOIN pg_class c ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'memos_graph'
          AND c.relname = 'cubes'
          AND p.relname = '_ag_label_vertex'
    ) INTO is_age_vertex;

    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'memos_graph'
          AND table_name = 'cubes'
          AND column_name = 'cube_id'
    ) INTO has_cube_id;

    -- If already plain table with cube_id, do nothing (idempotent on fresh-DB where 0001 ran cleanly).
    IF has_cube_id THEN
        RAISE NOTICE 'cubes already plain table with cube_id — skipping';
        RETURN;
    END IF;

    -- If AGE vertex, drop it. drop_vlabel is the AGE-native way.
    -- Fall back to DROP TABLE CASCADE if drop_vlabel fails (e.g., label considered in-use).
    IF is_age_vertex THEN
        RAISE NOTICE 'dropping AGE vertex cubes';
        BEGIN
            PERFORM ag_catalog.drop_vlabel('memos_graph', 'cubes');
        EXCEPTION WHEN OTHERS THEN
            RAISE NOTICE 'drop_vlabel failed (%), falling back to DROP TABLE CASCADE', SQLERRM;
            DROP TABLE IF EXISTS memos_graph.cubes CASCADE;
        END;
    ELSE
        -- Unknown shape — table exists but is neither plain-with-cube_id nor AGE vertex.
        -- Drop and recreate. This is safe for memdb-go (no real data in cubes yet).
        RAISE NOTICE 'cubes exists with unexpected shape — dropping';
        DROP TABLE IF EXISTS memos_graph.cubes CASCADE;
    END IF;
END
$$;

-- Recreate as plain table per migration 0001 design.
CREATE TABLE IF NOT EXISTS memos_graph.cubes (
    cube_id     TEXT        PRIMARY KEY,
    cube_name   TEXT        NOT NULL,
    owner_id    TEXT        NOT NULL,
    description TEXT,
    cube_path   TEXT,
    settings    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_active   BOOLEAN     NOT NULL DEFAULT TRUE
);

CREATE INDEX IF NOT EXISTS idx_cubes_owner      ON memos_graph.cubes (owner_id)             WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_cubes_path       ON memos_graph.cubes (cube_path)            WHERE cube_path IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_cubes_updated_at ON memos_graph.cubes (updated_at DESC)      WHERE is_active;
