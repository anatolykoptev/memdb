package queries

// queries_search_vector.go — vector similarity search SQL constants.
// Covers: VectorSearch, VectorSearchMultiCube, VectorSearchWithCutoff.

// VectorSearch performs cosine similarity search across multiple memory types using pgvector.
// The vector parameter $1 must be a text string literal (e.g. '[0.1,0.2,...]') cast to
// halfvec(1024). halfvec uses float16 storage — 2x smaller HNSW index than vector(1024).
// The Go code is responsible for formatting the embedding as a bracket-delimited string.
// ORDER BY uses the distance expression directly so the halfvec HNSW index is always used.
//
// Args: $1 = vector string literal (text, cast to halfvec(1024)),
//
//	$2 = user_name (text),
//	$3 = user_id (text),
//	$4 = memory_types (text[]),
//	$5 = limit (int),
//	$6 = agent_id (text, '' for any)
const VectorSearch = `
SELECT id::text,
       (properties - 'sources')::text,
       1 - (embedding::halfvec(1024) <=> $1::halfvec(1024)) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = $2
  AND properties->>'user_id'   = $3
  AND properties->>'memory_type' = ANY($4)
  AND ($6::text = '' OR properties->>'agent_id' = $6)
  AND embedding IS NOT NULL
ORDER BY embedding::halfvec(1024) <=> $1::halfvec(1024) ASC
LIMIT $5`

// VectorSearchMultiCube is VectorSearch across multiple cubes (user_names).
// Enables cross-domain search: the experience memory transfers learning from
// cube A to cube B when both are in the caller's readable_cube_ids list.
//
// Args: $1 = vector string literal (text, cast to halfvec(1024)),
//
//	$2 = user_names (text[]) — list of cube IDs to search across,
//	$3 = user_id (text),
//	$4 = memory_types (text[]),
//	$5 = limit (int),
//	$6 = agent_id (text, '' for any)
const VectorSearchMultiCube = `
SELECT id::text,
       (properties - 'sources')::text,
       1 - (embedding::halfvec(1024) <=> $1::halfvec(1024)) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = ANY($2::text[])
  AND properties->>'user_id'   = $3
  AND properties->>'memory_type' = ANY($4)
  AND ($6::text = '' OR properties->>'agent_id' = $6)
  AND embedding IS NOT NULL
ORDER BY embedding::halfvec(1024) <=> $1::halfvec(1024) ASC
LIMIT $5`

// VectorSearchWithCutoff is VectorSearch with an additional created_at filter for temporal scope.
//
// Args: $1 = vector string literal (text, cast to halfvec(1024)),
//
//	$2 = user_name (text),
//	$3 = user_id (text),
//	$4 = memory_types (text[]),
//	$5 = limit (int),
//	$6 = cutoff ISO timestamp (text),
//	$7 = agent_id (text, '' for any)
const VectorSearchWithCutoff = `
SELECT id::text,
       (properties - 'sources')::text,
       1 - (embedding::halfvec(1024) <=> $1::halfvec(1024)) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = $2
  AND properties->>'user_id'   = $3
  AND properties->>'memory_type' = ANY($4)
  AND ($7::text = '' OR properties->>'agent_id' = $7)
  AND embedding IS NOT NULL
  AND (properties->>'created_at') >= $6
ORDER BY embedding::halfvec(1024) <=> $1::halfvec(1024) ASC
LIMIT $5`
