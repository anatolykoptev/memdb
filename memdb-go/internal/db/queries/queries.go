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

// --- Schema ---

// InitAGE loads the Apache AGE extension and sets the search path.
const InitAGE = `LOAD 'age'`

// SetSearchPath configures the PostgreSQL search path for AGE + graph schema.
const SetSearchPath = `SET search_path = ag_catalog, "$1", public`

// --- Single Node Queries ---

// GetByID retrieves a single memory node by its table row ID.
// Args: $1 = row id (text)
const GetByID = `
SELECT id,
       properties,
       embedding
FROM "$1"."Memory"
WHERE id = $2
LIMIT 1`

// GetByPropertyID retrieves a node by its properties->>'id' field (UUID).
// Args: $1 = graph_name, $2 = property id (text), $3 = user_name (text)
const GetByPropertyID = `
SELECT id,
       properties,
       embedding
FROM %[1]s."Memory"
WHERE properties->>'id' = $1
  AND properties->>'user_name' = $2
LIMIT 1`

// GetByPropertyIDs retrieves multiple nodes by their properties->>'id' field.
// Args: $1 = property ids (text[]), $2 = user_name (text)
const GetByPropertyIDs = `
SELECT id,
       properties
FROM %[1]s."Memory"
WHERE properties->>'id' = ANY($1)
  AND properties->>'user_name' = $2`

// --- Batch Queries ---

// GetByRowIDs retrieves multiple nodes by their table row IDs.
// Args: $1 = row ids (text[])
const GetByRowIDs = `
SELECT id,
       properties
FROM %[1]s."Memory"
WHERE id = ANY($1)`

// --- Vector Search ---

// VectorSearch performs cosine similarity search on embeddings.
// Returns results ordered by descending similarity score.
// The score is computed as (1 - cosine_distance), normalized to [0, 1] via (score + 1) / 2.
// Args: $1 = embedding vector, $2 = user_name, $3 = memory_type, $4 = top_k
//
// Use fmt.Sprintf to inject graph_name (%[1]s) and optional WHERE clauses (%[2]s).
const VectorSearch = `
WITH t AS (
    SELECT id,
           properties,
           (1 - (embedding <=> $1::vector(1024))) AS score
    FROM %[1]s."Memory"
    WHERE embedding IS NOT NULL
      AND properties->>'status' = 'activated'
      %[2]s
    ORDER BY score DESC
    LIMIT $2
)
SELECT id, properties, score
FROM t
WHERE score > 0.1`

// --- Full-Text Search ---

// FulltextSearch performs full-text search using the tsvector column.
// Args: $1 = tsquery string (pipe-separated words: "word1 | word2")
// Returns results ranked by ts_rank score.
//
// Use fmt.Sprintf to inject graph_name (%[1]s) and optional WHERE clauses (%[2]s).
const FulltextSearch = `
SELECT id,
       properties,
       properties->>'memory' AS memory_text,
       ts_rank(properties_tsvector_zh, to_tsquery('simple', $1)) AS rank
FROM %[1]s."Memory"
WHERE properties_tsvector_zh @@ to_tsquery('simple', $1)
  AND properties->>'status' = 'activated'
  %[2]s
ORDER BY rank DESC
LIMIT $2`

// --- Count & Stats ---

// CountByType returns the count of memories grouped by memory_type and status.
// Use fmt.Sprintf to inject graph_name.
const CountByType = `
SELECT properties->>'memory_type' AS memory_type,
       properties->>'status' AS status,
       COUNT(*) AS count
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
GROUP BY properties->>'memory_type', properties->>'status'
ORDER BY memory_type, status`

// CountTotal returns total memory count for a user.
const CountTotal = `
SELECT COUNT(*)
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'status' = 'activated'`

// --- Delete ---

// DeleteByPropertyIDs deletes nodes by their properties->>'id' values.
// Args: $1 = property ids (text[]), $2 = user_name (text)
const DeleteByPropertyIDs = `
DELETE FROM %[1]s."Memory"
WHERE properties->>'id' = ANY($1)
  AND properties->>'user_name' = $2`

// --- Export / List ---

// ListMemories returns paginated memory nodes for export.
// Args: $1 = user_name, $2 = limit, $3 = offset
// Use fmt.Sprintf to inject graph_name and optional WHERE clause.
const ListMemories = `
SELECT id,
       properties,
       embedding
FROM %[1]s."Memory"
WHERE properties->>'user_name' = $1
  AND properties->>'status' = 'activated'
  %[2]s
ORDER BY (properties->>'created_at') DESC NULLS LAST, id DESC
LIMIT $2 OFFSET $3`

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
SELECT id,
       properties
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

// --- User Names by Memory IDs ---

// GetUserNamesByPropertyIDs maps property IDs to user names.
// Args: $1 = property ids (text[])
const GetUserNamesByPropertyIDs = `
SELECT properties->>'id' AS property_id,
       properties->>'user_name' AS user_name
FROM %[1]s."Memory"
WHERE properties->>'id' = ANY($1)`

// --- WHERE clause fragments ---

// FilterByMemoryType is a WHERE clause fragment to filter by memory_type.
// Append to queries using fmt.Sprintf.
const FilterByMemoryType = `AND properties->>'memory_type' = $%d`

// FilterByUserName is a WHERE clause fragment to filter by user_name.
const FilterByUserName = `AND properties->>'user_name' = $%d`
