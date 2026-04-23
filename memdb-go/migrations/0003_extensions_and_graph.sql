-- MemDB migration 0003: PostgreSQL extensions + AGE graph bootstrap
-- Date: 2026-04-23
-- Ports Python graph_dbs/polardb/schema.py create_extension() + create_graph().
-- Idempotent: CREATE EXTENSION IF NOT EXISTS, DO-block checks ag_catalog.ag_graph
-- before calling create_graph() (which would error on re-invocation).

CREATE EXTENSION IF NOT EXISTS age;
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM ag_catalog.ag_graph WHERE name = 'memos_graph') THEN
        PERFORM create_graph('memos_graph');
    END IF;
END
$$;
