package queries

// queries_memory_wm.go — SQL queries for WorkingMemory management.
// Covers: count, oldest-first fetch, recent-with-embedding fetch (VSET warm).
//
// NOTE: the Memory vertex's id column is an AGE-managed graphid. The stable UUID
// identity lives in properties->>(('id'::text)). Exposed memory_id columns return
// the property UUID so callers can feed IDs into DeleteByPropertyIDs / VRemBatch
// (both of which expect the property UUID).

// CountWorkingMemory returns the number of activated WorkingMemory nodes for a cube.
// Args: $1 = user_name (text)
const CountWorkingMemory = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('memory_type'::text)) = 'WorkingMemory'
  AND properties->>(('status'::text)) = 'activated'`

// GetWorkingMemoryOldestFirst returns activated WorkingMemory nodes for a cube
// ordered oldest-first (by updated_at ASC). Used by WM compaction to identify
// which nodes to summarize (oldest) vs keep (most recent).
// Args: $1 = user_name (text), $2 = limit (int)
const GetWorkingMemoryOldestFirst = `
SELECT
    properties->>(('id'::text))     AS memory_id,
    properties->>(('memory'::text)) AS memory_text
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('memory_type'::text)) = 'WorkingMemory'
  AND properties->>(('status'::text)) = 'activated'
ORDER BY (properties->>(('updated_at'::text))) ASC NULLS LAST
LIMIT $2`

// GetRecentWorkingMemory returns the N most-recent activated WorkingMemory nodes
// for a cube including their embeddings.  Used by WorkingMemoryCache.Sync to
// warm the VSET hot-cache on server startup.
// Args: $1 = user_name (text), $2 = limit (int)
const GetRecentWorkingMemory = `
SELECT
    properties->>(('id'::text))     AS memory_id,
    properties->>(('memory'::text)) AS memory_text,
    COALESCE(EXTRACT(EPOCH FROM (properties->>(('updated_at'::text)))::timestamptz)::bigint, 0) AS ts,
    embedding::text                 AS embedding_text
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('memory_type'::text)) = 'WorkingMemory'
  AND properties->>(('status'::text)) = 'activated'
  AND embedding IS NOT NULL
ORDER BY (properties->>(('updated_at'::text))) DESC NULLS LAST
LIMIT $2`
