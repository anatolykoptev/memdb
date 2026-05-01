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
//
// Returns the stable property UUID (properties->>'id'), NOT the AGE graphid —
// callers mix this ID with write-path / handler code which store property UUIDs.
const VectorSearch = `
SELECT properties->>(('id'::text)) AS memory_id,
       (properties::text::jsonb - 'sources')::text,
       1 - (embedding::halfvec(1024) <=> $1::halfvec(1024)) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>(('status'::text)) = 'activated'
  AND properties->>(('user_name'::text)) = $2
  AND properties->>(('user_id'::text))   = $3
  AND properties->>(('memory_type'::text)) = ANY($4)
  AND ($6::text = '' OR properties->>(('agent_id'::text)) = $6)
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
SELECT properties->>(('id'::text)) AS memory_id,
       (properties::text::jsonb - 'sources')::text,
       1 - (embedding::halfvec(1024) <=> $1::halfvec(1024)) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>(('status'::text)) = 'activated'
  AND properties->>(('user_name'::text)) = ANY($2::text[])
  AND properties->>(('user_id'::text))   = $3
  AND properties->>(('memory_type'::text)) = ANY($4)
  AND ($6::text = '' OR properties->>(('agent_id'::text)) = $6)
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
SELECT properties->>(('id'::text)) AS memory_id,
       (properties::text::jsonb - 'sources')::text,
       1 - (embedding::halfvec(1024) <=> $1::halfvec(1024)) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>(('status'::text)) = 'activated'
  AND properties->>(('user_name'::text)) = $2
  AND properties->>(('user_id'::text))   = $3
  AND properties->>(('memory_type'::text)) = ANY($4)
  AND ($7::text = '' OR properties->>(('agent_id'::text)) = $7)
  AND embedding IS NOT NULL
  AND (properties->>(('created_at'::text))) >= $6
ORDER BY embedding::halfvec(1024) <=> $1::halfvec(1024) ASC
LIMIT $5`

// SparseVectorSearch — SPLADE sparse-vector cosine via inner product.
// Mirror of VectorSearch shape but on sparse_embedding column. Returns
// `embedding::text` (DENSE column) for downstream parity with dense
// scanVectorSearchRows — caller doesn't need the sparse vector again.
//
// pgvector's sparsevec uses inner product (`<#>`) as native distance —
// SPLADE is non-negative term weighting where higher dot = more relevant.
// Score = - <#> because pgvector returns negative inner product (smaller
// = more similar by distance convention); we flip sign so larger=better,
// matching the dense `1 - <=>` shape.
//
// Args mirror VectorSearch:
//   $1 = sparse vector literal "{idx:val,...}/30522" cast to sparsevec
//   $2 = user_name, $3 = user_id, $4 = memory_types[],
//   $5 = limit, $6 = agent_id ('' for any)
//
// WHERE includes `sparse_embedding IS NOT NULL` so legacy rows pre-backfill
// transparently fall out of the sparse leg without hurting recall — they
// remain available via the dense leg.
const SparseVectorSearch = `
SELECT properties->>(('id'::text)) AS memory_id,
       (properties::text::jsonb - 'sources')::text,
       -(sparse_embedding <#> $1::sparsevec(30522)) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>(('status'::text)) = 'activated'
  AND properties->>(('user_name'::text)) = $2
  AND properties->>(('user_id'::text))   = $3
  AND properties->>(('memory_type'::text)) = ANY($4)
  AND ($6::text = '' OR properties->>(('agent_id'::text)) = $6)
  AND sparse_embedding IS NOT NULL
ORDER BY sparse_embedding <#> $1::sparsevec(30522) ASC
LIMIT $5`
