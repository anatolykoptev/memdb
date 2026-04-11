package queries

// queries_memory.go — SQL queries for memory node CRUD operations.
// Covers: user/instance queries, get-all, delete, update, user-names,
//         insert/dedup/cleanup (native add pipeline), working memory, LTM search.

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

// ListCubesByTag returns distinct cube IDs (user_name in node properties)
// where at least one activated memory has the given tag in properties->'tags'.
// Used by go-wowa to hydrate its knownCubes set at startup — it asks for
// cubes tagged "mode:raw" (experience memory marker).
//
// Args: $1 = tag (text)
const ListCubesByTag = `
SELECT DISTINCT properties->>'user_name' AS cube_id
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' IS NOT NULL
  AND properties->'tags' @> to_jsonb(ARRAY[$1]::text[])
ORDER BY cube_id`

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

// UpdateMemoryNodeFull updates the memory text, embedding, and updated_at for an existing activated node.
// Used by the fine-mode dedup-merge pipeline when JudgeDedupMerge returns action="update".
// Args: $1 = memory_id (properties->>'id'), $2 = new memory text, $3 = new embedding (text cast to vector(1024)), $4 = updated_at (text)
const UpdateMemoryNodeFull = `
UPDATE %[1]s."Memory"
SET properties = properties
    || jsonb_build_object('memory',     $2::text,
                          'updated_at', $4::text),
    embedding = CASE WHEN $3::text = '' THEN embedding ELSE $3::vector(1024) END
WHERE id = $1
  AND properties->>'status' = 'activated'`

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

// CleanupOldestWorkingMemoryReturning is like CleanupOldestWorkingMemory but
// returns the property UUID of each deleted node so callers can evict them from VSET.
// Args: $1 = user_name (text), $2 = keep_count (int)
const CleanupOldestWorkingMemoryReturning = `
DELETE FROM %[1]s."Memory"
WHERE id IN (
    SELECT id FROM %[1]s."Memory"
    WHERE properties->>'user_name' = $1
      AND properties->>'memory_type' = 'WorkingMemory'
      AND properties->>'status' = 'activated'
    ORDER BY (properties->>'updated_at') DESC NULLS LAST
    OFFSET $2
)
RETURNING id AS node_id`

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

// SearchLTMByVector returns the top-k activated LongTermMemory/UserMemory/EpisodicMemory nodes
// for a user sorted by cosine similarity to the given query embedding.
// Used by the Go mem_update handler to refresh WorkingMemory with relevant LTM.
// EpisodicMemory is included so WM-compacted session summaries surface in future queries.
// Args: $1 = user_name (text), $2 = query embedding (text cast to vector(1024)),
//
//	$3 = min_score (float64), $4 = limit (int)
const SearchLTMByVector = `
SELECT
    id                    AS memory_id,
    properties->>'memory' AS memory_text,
    1 - (embedding::halfvec(1024) <=> $2::halfvec(1024)) AS score,
    embedding::text       AS embedding_text
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'status' = 'activated'
  AND properties->>'memory_type' IN ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
  AND embedding IS NOT NULL
  AND 1 - (embedding::halfvec(1024) <=> $2::halfvec(1024)) >= $3
ORDER BY embedding::halfvec(1024) <=> $2::halfvec(1024) ASC
LIMIT $4`

// FindNearDuplicatesByIDs returns near-duplicate pairs restricted to a given set
// of memory IDs (cross-checked against the full activated pool for that user).
// Used by the mem_feedback handler to run targeted reorganization on memories
// the user flagged via feedback.
// EpisodicMemory included so compacted summaries can be merged if duplicated.
// Args: $1 = user_name (text), $2 = ids (text[]), $3 = similarity threshold (float64), $4 = limit (int)
const FindNearDuplicatesByIDs = `
SELECT
    a.id                    AS id_a,
    a.properties->>'memory' AS mem_a,
    b.id                    AS id_b,
    b.properties->>'memory' AS mem_b,
    1 - (a.embedding <=> b.embedding) AS score
FROM %[1]s."Memory" a
JOIN %[1]s."Memory" b ON a.id < b.id
WHERE a.properties->>'user_name' = $1
  AND b.properties->>'user_name' = $1
  AND a.properties->>'status' = 'activated'
  AND b.properties->>'status' = 'activated'
  AND a.properties->>'memory_type' IN ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
  AND b.properties->>'memory_type' IN ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
  AND (a.id = ANY($2) OR b.id = ANY($2))
  AND a.embedding IS NOT NULL
  AND b.embedding IS NOT NULL
  AND 1 - (a.embedding <=> b.embedding) >= $3
ORDER BY score DESC
LIMIT $4`

// SearchLTMByVectorSQL returns the SearchLTMByVector query string for testing.
func SearchLTMByVectorSQL() string { return SearchLTMByVector }

// FindNearDuplicatesSQL returns the FindNearDuplicates query string for testing.
func FindNearDuplicatesSQL() string { return FindNearDuplicates }

// FindNearDuplicates returns pairs of activated LongTermMemory/UserMemory/EpisodicMemory nodes
// for a given user whose cosine similarity exceeds the threshold.
// Used by the Go Memory Reorganizer (scheduler) to detect candidates for consolidation.
// EpisodicMemory included so compacted WM summaries can be merged if duplicated.
// Args: $1 = user_name (text), $2 = similarity threshold (float64), $3 = limit (int)
const FindNearDuplicates = `
SELECT
    a.id                    AS id_a,
    a.properties->>'memory' AS mem_a,
    b.id                    AS id_b,
    b.properties->>'memory' AS mem_b,
    1 - (a.embedding <=> b.embedding) AS score
FROM %[1]s."Memory" a
JOIN %[1]s."Memory" b ON a.id < b.id
WHERE a.properties->>'user_name' = $1
  AND b.properties->>'user_name' = $1
  AND a.properties->>'status' = 'activated'
  AND b.properties->>'status' = 'activated'
  AND a.properties->>'memory_type' IN ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
  AND b.properties->>'memory_type' IN ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
  AND a.embedding IS NOT NULL
  AND b.embedding IS NOT NULL
  AND 1 - (a.embedding <=> b.embedding) >= $2
ORDER BY score DESC
LIMIT $3`

// --- Filter-based GET (T5: native post_get_memory with complex filters) ---

// GetMemoriesByFilterSQL is a SQL template for fetching memories matching user_name
// conditions (OR-joined) AND filter conditions (AND-joined) with a LIMIT clause.
// The WHERE template takes no $N parameters — all conditions are inlined by the caller
// using the filter package (which escapes values at render time). The LIMIT value is
// also inlined (integer, validated to ≤1000 by the handler).
//
// Usage: fmt.Sprintf(GetMemoriesByFilterSQL, graphName, whereSQLLiteral, limitInt)
const GetMemoriesByFilterSQL = `
SELECT properties::text
FROM %[1]s."Memory"
WHERE %[2]s
LIMIT %[3]d`

// --- Admin: raw memory detection ---

// FindRawMemories returns activated LTM/UserMemory nodes that contain raw conversation
// patterns (role: [timestamp]: content). Used by the admin reprocess endpoint.
// Args: $1 = user_name, $2 = memory_types (text[]), $3 = limit
const FindRawMemories = `
SELECT properties->>'id'     AS prop_id,
       properties->>'memory' AS memory
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = ANY($2)
  AND properties->>'status' = 'activated'
  AND position('assistant: [20' IN properties->>'memory') > 0
ORDER BY (properties->>'created_at') ASC NULLS LAST
LIMIT $3`

// CountRawMemories returns the total count of raw conversation-window memories.
// Args: $1 = user_name, $2 = memory_types (text[])
const CountRawMemories = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'memory_type' = ANY($2)
  AND properties->>'status' = 'activated'
  AND position('assistant: [20' IN properties->>'memory') > 0`

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
