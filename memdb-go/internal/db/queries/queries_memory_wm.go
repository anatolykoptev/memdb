package queries

// queries_memory_wm.go — SQL queries for WorkingMemory management.
// Covers: count, oldest-first fetch, recent-with-embedding fetch (VSET warm).

// CountWorkingMemory returns the number of activated WorkingMemory nodes for a cube.
// Args: $1 = user_name (text)
const CountWorkingMemory = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = 'WorkingMemory'
  AND properties->>'status' = 'activated'`

// GetWorkingMemoryOldestFirst returns activated WorkingMemory nodes for a cube
// ordered oldest-first (by updated_at ASC). Used by WM compaction to identify
// which nodes to summarize (oldest) vs keep (most recent).
// Args: $1 = user_name (text), $2 = limit (int)
const GetWorkingMemoryOldestFirst = `
SELECT
    id                    AS memory_id,
    properties->>'memory' AS memory_text
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = 'WorkingMemory'
  AND properties->>'status' = 'activated'
ORDER BY (properties->>'updated_at') ASC NULLS LAST
LIMIT $2`

// GetRecentWorkingMemory returns the N most-recent activated WorkingMemory nodes
// for a cube including their embeddings.  Used by WorkingMemoryCache.Sync to
// warm the VSET hot-cache on server startup.
// Args: $1 = user_name (text), $2 = limit (int)
const GetRecentWorkingMemory = `
SELECT
    id                    AS memory_id,
    properties->>'memory' AS memory_text,
    COALESCE(EXTRACT(EPOCH FROM (properties->>'updated_at')::timestamptz)::bigint, 0) AS ts,
    embedding::text       AS embedding_text
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = 'WorkingMemory'
  AND properties->>'status' = 'activated'
  AND embedding IS NOT NULL
ORDER BY (properties->>'updated_at') DESC NULLS LAST
LIMIT $2`
