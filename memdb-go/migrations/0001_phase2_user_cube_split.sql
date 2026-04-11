-- MemDB Phase 2: user/cube split migration
-- Date: 2026-04-11
-- Predecessor commits: 6ebc4e1 (search path fix), 6bca528 (delete/add hygiene)
--
-- What this does:
--   1. Creates memos_graph.cubes table with 9 fields + 3 partial indexes
--   2. Backfills cubes rows from DISTINCT properties->>'user_name' in Memory
--   3. Backfills properties->>'user_id' = 'krolik' on all existing Memory rows
--      (single-user deployment — every memory row's person is 'krolik')
--   4. Invariant checks: cube row count matches distinct cube count in Memory
--
-- Idempotency: all DDL uses IF NOT EXISTS; inserts use ON CONFLICT DO NOTHING;
-- update only touches rows where user_id != 'krolik'. Safe to re-run.

BEGIN;

-- Step 1: cubes table + indexes
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

-- Step 2: Backfill cubes rows from distinct user_name values in Memory
INSERT INTO memos_graph.cubes (cube_id, cube_name, owner_id, created_at, updated_at, is_active)
SELECT
    properties->>'user_name' AS cube_id,
    properties->>'user_name' AS cube_name,
    'krolik'                 AS owner_id,
    MIN(created_at)          AS created_at,
    MAX(created_at)          AS updated_at,
    TRUE                     AS is_active
FROM memos_graph."Memory"
WHERE properties->>'user_name' IS NOT NULL
GROUP BY properties->>'user_name'
ON CONFLICT (cube_id) DO NOTHING;

-- Step 3: Fix user_id JSONB slot on all existing Memory rows
-- Single-user deployment: every row's person becomes 'krolik'
UPDATE memos_graph."Memory"
SET properties = properties || '{"user_id":"krolik"}'::jsonb
WHERE properties->>'user_name' IS NOT NULL
  AND (properties->>'user_id' IS NULL OR properties->>'user_id' != 'krolik');

-- Step 4: Invariant checks
DO $$
DECLARE
    mem_total      INTEGER;
    mem_person     INTEGER;
    cube_rows      INTEGER;
    distinct_cubes INTEGER;
BEGIN
    SELECT COUNT(*)                                                INTO mem_total      FROM memos_graph."Memory";
    SELECT COUNT(*)                                                INTO mem_person     FROM memos_graph."Memory" WHERE properties->>'user_id' = 'krolik';
    SELECT COUNT(*)                                                INTO cube_rows      FROM memos_graph.cubes;
    SELECT COUNT(DISTINCT properties->>'user_name')                INTO distinct_cubes FROM memos_graph."Memory";

    RAISE NOTICE 'migration: total memory rows        = %', mem_total;
    RAISE NOTICE 'migration: memory rows (krolik)     = %', mem_person;
    RAISE NOTICE 'migration: cubes table rows         = %', cube_rows;
    RAISE NOTICE 'migration: distinct cubes in Memory = %', distinct_cubes;

    IF cube_rows != distinct_cubes THEN
        RAISE EXCEPTION 'cube count mismatch: cubes=% Memory.distinct=%',
            cube_rows, distinct_cubes;
    END IF;

    IF mem_person != (SELECT COUNT(*) FROM memos_graph."Memory" WHERE properties->>'user_name' IS NOT NULL) THEN
        RAISE EXCEPTION 'some Memory rows with user_name still missing user_id backfill';
    END IF;
END $$;

COMMIT;
