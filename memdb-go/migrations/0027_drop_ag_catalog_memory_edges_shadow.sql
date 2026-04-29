-- MemDB migration 0027: drop the stale ag_catalog.memory_edges shadow table
-- Date: 2026-04-28
--
-- Root cause of M11.Y PageRank distribution collapse (bug report: 5 distinct
-- scores for 9014 entries). Migration 0005 created memory_edges unqualified
-- with search_path = ag_catalog, memos_graph, ... so it landed in ag_catalog.
-- Migration 0012 moved it to memos_graph via ALTER TABLE SET SCHEMA.
-- However the ag_catalog copy is NOT dropped by 0012, leaving a shadow table.
--
-- The Go pool sets search_path = ag_catalog, memos_graph, public on every
-- connection. Unqualified FROM memory_edges resolves to ag_catalog.memory_edges
-- (first in search_path). ag_catalog.memory_edges lacks the confidence column
-- added in migration 0013, so FetchEdgesForPageRank fails with
--   ERROR: column e.confidence does not exist
-- causing 0 edges returned → PageRank not updated → stale/uniform scores.
--
-- Fix: drop the stale shadow. All go-* code now uses memos_graph.memory_edges
-- explicitly (qualified in SQL queries as %[1]s.memory_edges).
-- Idempotent: IF EXISTS guards prevent failure on a fresh DB (never had the
-- shadow) or re-runs.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_tables
        WHERE schemaname = 'ag_catalog' AND tablename = 'memory_edges'
    ) THEN
        -- Verify memos_graph.memory_edges exists before dropping the shadow
        -- to prevent data loss on misconfigured DBs.
        IF NOT EXISTS (
            SELECT 1 FROM pg_tables
            WHERE schemaname = 'memos_graph' AND tablename = 'memory_edges'
        ) THEN
            RAISE EXCEPTION 'memos_graph.memory_edges not found; refusing to drop ag_catalog shadow';
        END IF;

        DROP TABLE ag_catalog.memory_edges;
        RAISE NOTICE 'dropped stale ag_catalog.memory_edges shadow table';
    ELSE
        RAISE NOTICE 'ag_catalog.memory_edges does not exist — nothing to drop';
    END IF;
END
$$;
