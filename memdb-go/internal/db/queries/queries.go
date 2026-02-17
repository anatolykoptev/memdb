// Package queries contains SQL query constants for PolarDB (PostgreSQL + Apache AGE).
//
// PolarDB uses Apache AGE for graph operations on top of PostgreSQL.
// The main table is "{graph_name}"."Memory" where graph_name defaults to "memos_graph".
// Node properties are stored in a JSONB `properties` column.
// Vector embeddings are stored in `embedding` (vector(1024) for voyage-4-lite).
// Full-text search uses `properties_tsvector_zh` tsvector column with a GIN index.
package queries

// Default graph name used by MemDB.
const DefaultGraphName = "memos_graph"

// --- User & Instance Queries ---

// ListUsers returns distinct user names from activated memories.
const ListUsers = `
SELECT DISTINCT properties->>'user_name' AS user_name
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' IS NOT NULL
ORDER BY user_name`

// CountDistinctUsers returns the number of distinct user names with activated memories.
const CountDistinctUsers = `
SELECT COUNT(DISTINCT properties->>'user_name')
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'`

// ExistUser checks if a user has any activated memories.
// Args: $1 = user_name (text)
const ExistUser = `
SELECT COUNT(*) > 0
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'status' = 'activated'
LIMIT 1`

// --- Get All Memories ---

// GetAllMemories returns paginated memories for a user filtered by memory_type.
// Args: $1 = user_name, $2 = memory_type, $3 = limit, $4 = offset
const GetAllMemories = `
SELECT id::text,
       properties::text
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = $2
  AND properties->>'status' = 'activated'
ORDER BY (properties->>'created_at') DESC NULLS LAST, id DESC
LIMIT $3 OFFSET $4`

// CountByUserAndType returns total count for a user/type combo.
// Args: $1 = user_name, $2 = memory_type
const CountByUserAndType = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = $2
  AND properties->>'status' = 'activated'`

// --- Delete ---

// DeleteByPropertyIDs deletes nodes by their properties->>'id' values.
// Args: $1 = property ids (text[]), $2 = user_name (text)
const DeleteByPropertyIDs = `
DELETE FROM %[1]s."Memory"
WHERE properties->>'id' = ANY($1)
  AND properties->>'user_name' = $2`

// --- Update ---

// UpdateMemoryContent updates the memory text in a node's properties JSONB.
// Args: $1 = memory_id (properties->>'id'), $2 = new content (text)
const UpdateMemoryContent = `
UPDATE %[1]s."Memory"
SET properties = jsonb_set(properties, '{memory}', to_jsonb($2::text))
WHERE properties->>'id' = $1
  AND properties->>'status' = 'activated'`

// DeleteAllByUser deletes all activated memories for a user.
// Args: $1 = user_name (text)
const DeleteAllByUser = `
DELETE FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'status' = 'activated'`

// --- User Names by Memory IDs ---

// GetUserNamesByPropertyIDs maps property IDs to user names.
// Args: $1 = property ids (text[])
const GetUserNamesByPropertyIDs = `
SELECT properties->>'id' AS property_id,
       properties->>'user_name' AS user_name
FROM %[1]s."Memory"
WHERE properties->>'id' = ANY($1)`

// --- Insert / Dedup / Cleanup (Phase 3: native add) ---

// DeleteMemoryByPropID deletes a memory node by its table id (which equals properties->>'id').
// Used as the first half of an upsert (DELETE then INSERT).
// Args: $1 = id (text, UUID)
const DeleteMemoryByPropID = `
DELETE FROM %[1]s."Memory" WHERE id = $1`

// InsertMemoryNode inserts a new memory node with id, properties and embedding.
// The table id column must match properties->>'id'.
// Used as the second half of an upsert (DELETE then INSERT).
// Args: $1 = id (text), $2 = properties (jsonb), $3 = embedding (text, cast to vector(1024))
const InsertMemoryNode = `
INSERT INTO %[1]s."Memory"(id, properties, embedding)
VALUES ($1, $2::jsonb, $3::vector(1024))`

// CheckContentHashExists checks whether an activated memory with the given content_hash exists for a user.
// Args: $1 = content_hash (text), $2 = user_name (text)
const CheckContentHashExists = `
SELECT EXISTS(
    SELECT 1 FROM %[1]s."Memory"
    WHERE properties->'info'->>'content_hash' = $1
      AND properties->>'user_name' = $2
      AND properties->>'status' = 'activated'
)`

// CleanupOldestWorkingMemory deletes the oldest WorkingMemory nodes beyond a keep limit.
// Keeps the N newest (by updated_at DESC) and deletes the rest.
// Args: $1 = user_name (text), $2 = keep_count (int)
const CleanupOldestWorkingMemory = `
DELETE FROM %[1]s."Memory"
WHERE id IN (
    SELECT id FROM %[1]s."Memory"
    WHERE properties->>'user_name' = $1
      AND properties->>'memory_type' = 'WorkingMemory'
      AND properties->>'status' = 'activated'
    ORDER BY (properties->>'updated_at') DESC NULLS LAST
    OFFSET $2
)`
