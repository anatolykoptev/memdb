package queries

// queries_memory_crud.go — SQL queries for memory node read/update/delete.
// Covers: get-all, get-by-id(s), delete, update, user-names mapping.
//
// NOTE: the Memory vertex's id column is an AGE-managed graphid. The stable UUID
// identity lives in properties->>(('id'::text)). All lookups match by the property
// UUID, and SELECT-exposed memory_id / mem_id / node_id aliases return the property
// UUID so downstream DELETE/UPDATE-by-UUID paths stay consistent.

// --- Get All Memories ---

// GetAllMemories returns paginated memories for a user filtered by memory_type.
// Args: $1 = user_name, $2 = memory_type, $3 = limit, $4 = offset
const GetAllMemories = `
SELECT properties->>(('id'::text)) AS memory_id,
       properties::text
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('memory_type'::text)) = $2
  AND properties->>(('status'::text)) = 'activated'
ORDER BY (properties->>(('created_at'::text))) DESC NULLS LAST, id DESC
LIMIT $3 OFFSET $4`

// GetAllMemoriesByTypes returns paginated memories for a user filtered by multiple memory_type values.
// Used by NativePostGetMemory to fetch text_mem (LongTermMemory + UserMemory) in one query.
// Args: $1 = user_name, $2 = memory_types (text[]), $3 = limit, $4 = offset
const GetAllMemoriesByTypes = `
SELECT properties->>(('id'::text)) AS memory_id,
       properties::text
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('memory_type'::text)) = ANY($2)
  AND properties->>(('status'::text)) = 'activated'
ORDER BY (properties->>(('created_at'::text))) DESC NULLS LAST, id DESC
LIMIT $3 OFFSET $4`

// CountByUserAndTypes returns total count for a user across multiple memory types.
// Args: $1 = user_name, $2 = memory_types (text[])
const CountByUserAndTypes = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('memory_type'::text)) = ANY($2)
  AND properties->>(('status'::text)) = 'activated'`

// CountByUserAndType returns total count for a user/type combo.
// Args: $1 = user_name, $2 = memory_type
const CountByUserAndType = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('memory_type'::text)) = $2
  AND properties->>(('status'::text)) = 'activated'`

// GetMemoryByPropertyIDs retrieves memory nodes by their properties->>(('id'::text)) UUIDs.
// Used by mem_feedback and mem_read handlers to fetch memory texts for LLM analysis.
// Args: $1 = property ids (text[]), $2 = user_name (text)
const GetMemoryByPropertyIDs = `
SELECT properties->>(('id'::text))          AS mem_id,
       properties->>(('memory'::text))      AS mem_text
FROM %[1]s."Memory"
WHERE properties->>(('id'::text)) = ANY($1)
  AND properties->>(('user_name'::text)) = $2
  AND properties->>(('status'::text)) = 'activated'`

// GetMemoryByPropertyID retrieves a single memory node by its property UUID (properties->>(('id'::text))).
// Used by GET /product/get_memory/{memory_id} native handler.
// Args: $1 = property id (text)
const GetMemoryByPropertyID = `
SELECT properties->>(('id'::text)) AS memory_id,
       properties::text             AS properties
FROM %[1]s."Memory"
WHERE properties->>(('id'::text)) = $1
  AND properties->>(('status'::text)) = 'activated'
LIMIT 1`

// GetMemoriesByPropertyIDs retrieves full memory nodes by their properties->>(('id'::text)) UUIDs.
// No user_name filter — UUIDs are globally unique; used by read-only get_memory_by_ids handler.
// Args: $1 = property ids (text[])
const GetMemoriesByPropertyIDs = `
SELECT properties->>(('id'::text)) AS memory_id,
       properties::text             AS properties
FROM %[1]s."Memory"
WHERE properties->>(('id'::text)) = ANY($1)
  AND properties->>(('status'::text)) = 'activated'`

// --- Delete ---

// DeleteByPropertyIDs deletes nodes by their properties->>(('id'::text)) values.
// Args: $1 = property ids (text[]), $2 = user_name (text)
const DeleteByPropertyIDs = `
DELETE FROM %[1]s."Memory"
WHERE properties->>(('id'::text)) = ANY($1)
  AND properties->>(('user_name'::text)) = $2`

// --- Update ---

// UpdateMemoryContent updates the memory text in a node's properties JSONB.
// Args: $1 = memory_id (properties->>(('id'::text))), $2 = new content (text)
const UpdateMemoryContent = `
UPDATE %[1]s."Memory"
SET properties = (jsonb_set(properties::text::jsonb, '{memory}', to_jsonb($2::text))::text)::agtype
WHERE properties->>(('id'::text)) = $1
  AND properties->>(('status'::text)) = 'activated'`

// SoftDeleteMerged marks a memory as merged into another, following MemOS lifecycle:
//
//	activated → merged (not deleted — still queryable for audit/history)
//
// Sets: status="merged", merged_into_id=<winner_id>, updated_at=<now>
// Args: $1 = memory_id (properties->>(('id'::text))), $2 = merged_into_id (text), $3 = updated_at (text)
const SoftDeleteMerged = `
UPDATE %[1]s."Memory"
SET properties = (
        (properties::text::jsonb || jsonb_build_object(
            'status',         'merged',
            'merged_into_id', $2::text,
            'updated_at',     $3::text
        ))::text
    )::agtype
WHERE properties->>(('id'::text)) = $1
  AND properties->>(('status'::text)) = 'activated'`

// DeleteAllByUser deletes all activated memories for a user.
// Args: $1 = user_name (text)
const DeleteAllByUser = `
DELETE FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('status'::text)) = 'activated'`

// --- User Names by Memory IDs ---

// GetUserNamesByPropertyIDs maps property IDs to user names.
// Args: $1 = property ids (text[])
const GetUserNamesByPropertyIDs = `
SELECT properties->>(('id'::text))        AS property_id,
       properties->>(('user_name'::text)) AS user_name
FROM %[1]s."Memory"
WHERE properties->>(('id'::text)) = ANY($1)`

// UpdateMemoryPropsAndEmbedding replaces the properties JSONB blob AND the
// embedding vector for a single memory node, scoped to (property UUID, user_name).
// Used by NativeUpdateMemory to atomically rewrite a memory without the
// delete-then-add race window.
//
// Args: $1 = memory_id (text, UUID = properties->>(('id'::text))),
//       $2 = user_name (cube id),
//       $3 = properties JSON (bytes),
//       $4 = embedding vector literal (text)
const UpdateMemoryPropsAndEmbedding = `
UPDATE %[1]s."Memory"
SET properties = $3::text::agtype,
    embedding  = $4::halfvec(1024)
WHERE properties->>(('id'::text)) = $1
  AND properties->>(('user_name'::text)) = $2
RETURNING properties->>(('id'::text))`

// --- CE pre-compute (M10 Stream 6) ---

// SetCEScoresTopK persists pre-computed cross-encoder top-K neighbour scores
// for a single memory under properties->>'ce_score_topk' as a JSON array
// [{"neighbor_id": "<uuid>", "score": 0.85}, ...] sorted by score DESC.
// Stored via jsonb_set inside the existing properties blob to keep the
// round-trip to a single statement and avoid a schema migration.
//
// Args: $1 = memory_id (text, UUID), $2 = user_name (cube id),
//       $3 = ce_score_topk JSON array as text
const SetCEScoresTopK = `
UPDATE %[1]s."Memory"
SET properties = (jsonb_set(properties::text::jsonb, '{ce_score_topk}', $3::jsonb)::text)::agtype
WHERE properties->>(('id'::text)) = $1
  AND properties->>(('user_name'::text)) = $2
  AND properties->>(('status'::text)) = 'activated'`

// ClearCEScoresTopK removes the ce_score_topk key for a single memory by
// UUID alone (UUIDs are globally unique — no need to scope by user_name).
// Called from applyUpdateAction when memory text changes — cached pairwise
// scores no longer reflect the new content.
//
// Args: $1 = memory_id (text, UUID)
const ClearCEScoresTopK = `
UPDATE %[1]s."Memory"
SET properties = ((properties::text::jsonb - 'ce_score_topk')::text)::agtype
WHERE properties->>(('id'::text)) = $1`

// ClearCEScoresTopKForNeighbor cascades cache invalidation: any memory
// whose ce_score_topk JSON array contains the given neighbor_id has its
// cache cleared (because the neighbour was updated or deleted, so the
// cached pairwise score against it is now stale or dangling). Single SQL
// UPDATE — no per-row loop in Go.
//
// Args: $1 = neighbor_id (text, UUID)
const ClearCEScoresTopKForNeighbor = `
UPDATE %[1]s."Memory"
SET properties = ((properties::text::jsonb - 'ce_score_topk')::text)::agtype
WHERE properties::text::jsonb -> 'ce_score_topk' @> jsonb_build_array(jsonb_build_object('neighbor_id', $1::text))`
