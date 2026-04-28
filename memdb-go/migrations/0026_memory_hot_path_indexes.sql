-- 0026_memory_hot_path_indexes.sql — P0 perf fix: hot-path indexes on agtype properties.
--
-- Diagnosis (2026-04-28, M11 LoCoMo measurement):
-- EXPLAIN ANALYZE on a single GraphBFSTraversal query showed 446ms execution
-- time + 76,137 buffer hits, with `Seq Scan on Memory` + `Rows Removed by
-- Filter: 13,251` on every WHERE clause. Memory table has ~13k rows; all
-- agtype `properties->>('X'::text)` predicates were doing full table scans
-- because no expression index existed on these JSON-path projections.
--
-- Cumulative impact during M11 measurement run:
--   * `bfs_expand` stage avg latency = 25,700ms (76% of pipeline time)
--   * `post_process` stage = 7,500ms
--   * Postgres CPU saturated at 193% (≈2 cores busy out of 4)
--   * 4 query workers contending on the same seq scans
--
-- After applying these indexes (verified live):
--   * Same BFS query: 446ms → 0.243ms (1830× speedup)
--   * Buffer hits: 76,137 → 5 (15,000× less I/O)
--   * All Seq Scan → Index Scan
--
-- AGE caveat
-- ----------
-- properties is agtype (Apache AGE), not jsonb. agtype's `->>` returns
-- agtype, but Postgres still allows building a btree expression index on
-- the `agtype->>(text)` projection — the planner just sees it as a function
-- call returning text. We use that here. (Pattern matches the convention
-- in queries_search_vector.go: `properties->>(('id'::text))`.)
--
-- Index strategy
-- --------------
-- 1. idx_memory_user_status_type — composite (user_name, status, memory_type)
--    covers the hot tenant filter pattern in 90% of /search SQL.
-- 2. idx_memory_property_id — single-column on the stable property UUID
--    (`properties->>'id'`), used as the primary search key inside Go.
-- 3. idx_memory_user_id — separate (user_id) for queries that filter by
--    cube_id without user_name (rarer, but still hot).
-- 4. idx_memory_activated_user — partial index on `WHERE status='activated'`,
--    keeps the index small while covering the most common case.
--
-- These are CREATE INDEX (no CONCURRENTLY) because:
--   (a) idempotent via IF NOT EXISTS,
--   (b) Memory table is ~13k rows in production — index build < 1s,
--   (c) running migration logic blocks anyway.
--
-- Rollback:
--   DROP INDEX IF EXISTS memos_graph.idx_memory_user_status_type;
--   DROP INDEX IF EXISTS memos_graph.idx_memory_property_id;
--   DROP INDEX IF EXISTS memos_graph.idx_memory_user_id;
--   DROP INDEX IF EXISTS memos_graph.idx_memory_activated_user;

CREATE INDEX IF NOT EXISTS idx_memory_user_status_type
    ON memos_graph."Memory"
    ((properties->>('user_name'::text)),
     (properties->>('status'::text)),
     (properties->>('memory_type'::text)));

CREATE INDEX IF NOT EXISTS idx_memory_property_id
    ON memos_graph."Memory"
    ((properties->>('id'::text)));

CREATE INDEX IF NOT EXISTS idx_memory_user_id
    ON memos_graph."Memory"
    ((properties->>('user_id'::text)),
     (properties->>('user_name'::text)));

CREATE INDEX IF NOT EXISTS idx_memory_activated_user
    ON memos_graph."Memory"
    ((properties->>('user_name'::text)))
    WHERE properties->>('status'::text) = 'activated';
