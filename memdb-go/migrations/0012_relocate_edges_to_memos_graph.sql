-- MemDB migration 0012: relocate memory_edges/entity_*/user_configs from ag_catalog to memos_graph
-- Date: 2026-04-24
-- B1 migrations (0005-0008) created these tables unqualified. Search_path
-- was `ag_catalog, memos_graph, "$user", public` at migration time, so the
-- tables landed in `ag_catalog` — the first writable schema in that list.
-- Go queries reference `memos_graph.<table>` and silently return 0 rows.
-- This broke D2 multi-hop retrieval and the entity graph recall paths.
--
-- Fix: ALTER TABLE ... SET SCHEMA for each of the four tables. SET SCHEMA
-- preserves data, indexes, constraints, and foreign keys atomically. Safe
-- because:
-- - Tables exist only in ag_catalog (verified via pg_tables join before)
-- - memos_graph.<name> does not already exist (verified — schema conflict impossible)
-- - No other queries read these via ag_catalog.<name> (Go always uses memos_graph)
-- Idempotent: DO-block checks current schema before moving.

DO $$
DECLARE
    t text;
    tables text[] := ARRAY['memory_edges', 'entity_edges', 'entity_nodes', 'user_configs'];
    current_schema text;
BEGIN
    FOREACH t IN ARRAY tables LOOP
        SELECT schemaname INTO current_schema FROM pg_tables
        WHERE tablename = t
        ORDER BY CASE schemaname
            WHEN 'memos_graph' THEN 1
            WHEN 'ag_catalog'  THEN 2
            ELSE 3
        END
        LIMIT 1;

        IF current_schema = 'memos_graph' THEN
            RAISE NOTICE 'table % already in memos_graph — skipping', t;
        ELSIF current_schema = 'ag_catalog' THEN
            EXECUTE format('ALTER TABLE ag_catalog.%I SET SCHEMA memos_graph', t);
            RAISE NOTICE 'moved % from ag_catalog to memos_graph', t;
        ELSIF current_schema IS NULL THEN
            RAISE NOTICE 'table % does not exist — skipping (fresh DB)', t;
        ELSE
            RAISE EXCEPTION 'table % in unexpected schema %; manual review required',
                t, current_schema;
        END IF;
    END LOOP;
END
$$;
