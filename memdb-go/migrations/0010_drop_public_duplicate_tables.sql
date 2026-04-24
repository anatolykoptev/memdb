-- MemDB migration 0010: drop legacy public.* duplicate tables
-- Date: 2026-04-24
-- B1 audit discovered that memory_edges / entity_edges / user_configs existed
-- both in memos_graph (current source of truth, from migrations 0005-0008) and
-- in public (empty legacy from an earlier Ensure* variant that used unqualified
-- names + different search_path). The public.* copies are unused by current Go
-- code, empty (0 rows verified 2026-04-24), and pollute \dt output.
-- Idempotent via IF EXISTS. Safe: guard counts before drop just in case.

DO $$
DECLARE
    n_memory_edges INTEGER := 0;
    n_entity_edges INTEGER := 0;
    n_user_configs INTEGER := 0;
BEGIN
    -- Count rows in public.* duplicates (silently 0 if table doesn't exist).
    BEGIN
        EXECUTE 'SELECT count(*) FROM public.memory_edges' INTO n_memory_edges;
    EXCEPTION WHEN undefined_table THEN
        n_memory_edges := 0;
    END;
    BEGIN
        EXECUTE 'SELECT count(*) FROM public.entity_edges' INTO n_entity_edges;
    EXCEPTION WHEN undefined_table THEN
        n_entity_edges := 0;
    END;
    BEGIN
        EXECUTE 'SELECT count(*) FROM public.user_configs' INTO n_user_configs;
    EXCEPTION WHEN undefined_table THEN
        n_user_configs := 0;
    END;

    IF n_memory_edges + n_entity_edges + n_user_configs > 0 THEN
        RAISE EXCEPTION 'public.* duplicate tables are NOT empty (memory_edges=%, entity_edges=%, user_configs=%). Inspect before dropping.',
            n_memory_edges, n_entity_edges, n_user_configs;
    END IF;

    RAISE NOTICE 'dropping empty public.* duplicates';
END
$$;

DROP TABLE IF EXISTS public.memory_edges;
DROP TABLE IF EXISTS public.entity_edges;
DROP TABLE IF EXISTS public.user_configs;
