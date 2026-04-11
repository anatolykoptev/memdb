package queries

// queries_memory_crud.go — SQL queries for memory node read/update/delete.
// Covers: get-all, get-by-id(s), delete, update, user-names mapping.

// --- Get All Memories ---

// GetAllMemories returns paginated memories for a user filtered by memory_type.
// Args: $1 = user_name, $2 = memory_type, $3 = limit, $4 = offset
const GetAllMemories = `
SELECT id          AS memory_id,
       properties::text
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = $2
  AND properties->>'status' = 'activated'
ORDER BY (properties->>'created_at') DESC NULLS LAST, id DESC
LIMIT $3 OFFSET $4`

// GetAllMemoriesByTypes returns paginated memories for a user filtered by multiple memory_type values.
// Used by NativePostGetMemory to fetch text_mem (LongTermMemory + UserMemory) in one query.
// Args: $1 = user_name, $2 = memory_types (text[]), $3 = limit, $4 = offset
const GetAllMemoriesByTypes = `
SELECT id          AS memory_id,
       properties::text
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = ANY($2)
  AND properties->>'status' = 'activated'
ORDER BY (properties->>'created_at') DESC NULLS LAST, id DESC
LIMIT $3 OFFSET $4`

// CountByUserAndTypes returns total count for a user across multiple memory types.
// Args: $1 = user_name, $2 = memory_types (text[])
const CountByUserAndTypes = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = ANY($2)
  AND properties->>'status' = 'activated'`

// CountByUserAndType returns total count for a user/type combo.
// Args: $1 = user_name, $2 = memory_type
const CountByUserAndType = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = $2
  AND properties->>'status' = 'activated'`

// GetMemoryByPropertyIDs retrieves memory nodes by their properties->>'id' UUIDs.
// Used by mem_feedback and mem_read handlers to fetch memory texts for LLM analysis.
// Args: $1 = property ids (text[]), $2 = user_name (text)
const GetMemoryByPropertyIDs = `
SELECT id                    AS mem_id,
       properties->>'memory'  AS mem_text
FROM %[1]s."Memory"
WHERE id = ANY($1)
  AND properties->>'user_name' = $2
  AND properties->>'status' = 'activated'`

// GetMemoryByPropertyID retrieves a single memory node by its property UUID (properties->>'id').
// Used by GET /product/get_memory/{memory_id} native handler.
// Args: $1 = property id (text)
const GetMemoryByPropertyID = `
SELECT id                 AS memory_id,
       properties::text    AS properties
FROM %[1]s."Memory"
WHERE id = $1
  AND properties->>'status' = 'activated'
LIMIT 1`

// GetMemoriesByPropertyIDs retrieves full memory nodes by their properties->>'id' UUIDs.
// No user_name filter — UUIDs are globally unique; used by read-only get_memory_by_ids handler.
// Args: $1 = property ids (text[])
const GetMemoriesByPropertyIDs = `
SELECT id                      AS memory_id,
       properties::text          AS properties
FROM %[1]s."Memory"
WHERE id = ANY($1)
  AND properties->>'status' = 'activated'`

// --- Delete ---

// DeleteByPropertyIDs deletes nodes by their properties->>'id' values.
// Args: $1 = property ids (text[]), $2 = user_name (text)
const DeleteByPropertyIDs = `
DELETE FROM %[1]s."Memory"
WHERE id = ANY($1)
  AND properties->>'user_name' = $2`

// --- Update ---

// UpdateMemoryContent updates the memory text in a node's properties JSONB.
// Args: $1 = memory_id (properties->>'id'), $2 = new content (text)
const UpdateMemoryContent = `
UPDATE %[1]s."Memory"
SET properties = jsonb_set(properties, '{memory}', to_jsonb($2::text))
WHERE id = $1
  AND properties->>'status' = 'activated'`

// SoftDeleteMerged marks a memory as merged into another, following MemOS lifecycle:
//
//	activated → merged (not deleted — still queryable for audit/history)
//
// Sets: status="merged", merged_into_id=<winner_id>, updated_at=<now>
// Args: $1 = memory_id (properties->>'id'), $2 = merged_into_id (text), $3 = updated_at (text)
const SoftDeleteMerged = `
UPDATE %[1]s."Memory"
SET properties = properties
    || jsonb_build_object(
        'status',         'merged',
        'merged_into_id', $2::text,
        'updated_at',     $3::text
    )
WHERE id = $1
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
SELECT id AS property_id,
       properties->>'user_name' AS user_name
FROM %[1]s."Memory"
WHERE id = ANY($1)`

// UpdateMemoryPropsAndEmbedding replaces the properties JSONB blob AND the
// embedding vector for a single memory node, scoped to (id, user_name).
// The table id column equals properties->>'id' (UUID), so we filter by id
// directly (same pattern as DeleteByPropertyIDs, GetMemoryByPropertyID).
// Used by NativeUpdateMemory to atomically rewrite a memory without the
// delete-then-add race window.
//
// Args: $1 = memory_id (text, UUID = table id), $2 = user_name (cube id),
//
//	$3 = properties JSON (bytes), $4 = embedding vector literal (text)
const UpdateMemoryPropsAndEmbedding = `
UPDATE %[1]s."Memory"
SET properties = $3::jsonb,
    embedding  = $4::halfvec(1024)
WHERE id = $1
  AND properties->>'user_name' = $2
RETURNING id`
