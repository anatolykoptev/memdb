package queries

// --- Search Queries ---

// VectorSearch performs cosine similarity search across multiple memory types using pgvector.
// The vector parameter $1 must be a text string literal (e.g. '[0.1,0.2,...]') cast to
// halfvec(1024). halfvec uses float16 storage — 2x smaller HNSW index than vector(1024).
// The Go code is responsible for formatting the embedding as a bracket-delimited string.
// ORDER BY uses the distance expression directly so the halfvec HNSW index is always used.
//
// Args: $1 = vector string literal (text, cast to halfvec(1024)),
//
//	$2 = user_name (text),
//	$3 = memory_types (text[]),
//	$4 = limit (int)
const VectorSearch = `
SELECT id::text,
       properties::text,
       1 - (embedding::halfvec(1024) <=> $1::halfvec(1024)) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = $2
  AND properties->>'memory_type' = ANY($3)
  AND ($5::text = '' OR properties->>'agent_id' = $5)
  AND embedding IS NOT NULL
ORDER BY embedding::halfvec(1024) <=> $1::halfvec(1024) ASC
LIMIT $4`

// FulltextSearch performs tsvector fulltext search on the properties_tsvector_zh column.
// The tsquery parameter $1 should be a properly formatted tsquery string for the 'simple'
// configuration (e.g. 'word1 & word2'). Results are ranked by ts_rank.
//
// Args: $1 = tsquery string (text),
//
//	$2 = user_name (text),
//	$3 = memory_types (text[]),
//	$4 = limit (int)
const FulltextSearch = `
SELECT id::text,
       properties::text,
       ts_rank(properties_tsvector_zh, to_tsquery('simple', $1)) AS rank
FROM %[1]s."Memory"
WHERE properties_tsvector_zh @@ to_tsquery('simple', $1)
  AND properties->>'status' = 'activated'
  AND properties->>'user_name' = $2
  AND properties->>'memory_type' = ANY($3)
  AND ($5::text = '' OR properties->>'agent_id' = $5)
ORDER BY rank DESC
LIMIT $4`

// GraphRecallByKey finds memory nodes whose properties->>'key' matches any of the given tokens.
// Returns id, properties and a fixed score (no vector similarity).
//
// Args: $1 = user_name (text),
//
//	$2 = memory_types (text[]),
//	$3 = keys (text[]),
//	$4 = limit (int)
const GraphRecallByKey = `
SELECT id::text,
       properties::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = $1
  AND properties->>'memory_type' = ANY($2)
  AND properties->>'key' = ANY($3)
  AND ($5::text = '' OR properties->>'agent_id' = $5)
LIMIT $4`

// GraphRecallByTags finds memory nodes whose tags array overlaps with given tags.
// Uses JSONB containment: for each candidate tag, checks if the tags array contains it.
// Post-filter in Go ensures overlap >= 2.
//
// Args: $1 = user_name (text),
//
//	$2 = memory_types (text[]),
//	$3 = tags (text[]) — candidate tags to check overlap,
//	$4 = limit (int)
const GraphRecallByTags = `
SELECT id::text,
       properties::text,
       tag_overlap
FROM (
    SELECT id, properties,
           array_length(
               ARRAY(
                   SELECT unnest(ARRAY(SELECT jsonb_array_elements_text(properties->'tags')))
                   INTERSECT
                   SELECT unnest($3::text[])
               ), 1
           ) AS tag_overlap
    FROM %[1]s."Memory"
    WHERE properties->>'status' = 'activated'
      AND properties->>'user_name' = $1
      AND properties->>'memory_type' = ANY($2)
      AND ($5::text = '' OR properties->>'agent_id' = $5)
      AND properties->'tags' IS NOT NULL
      AND jsonb_array_length(properties->'tags') > 0
) sub
WHERE tag_overlap >= 2
ORDER BY tag_overlap DESC
LIMIT $4`

// GetWorkingMemory returns all activated WorkingMemory items for a user.
// No vector/fulltext search — just a filtered SELECT ordered by recency.
// Returns embeddings so callers can compute cosine similarity vs query.
//
// Args: $1 = user_name (text),
//
//	$2 = limit (int)
const GetWorkingMemory = `
SELECT id::text,
       properties::text,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = $1
  AND properties->>'memory_type' = 'WorkingMemory'
  AND ($3::text = '' OR properties->>'agent_id' = $3)
  AND embedding IS NOT NULL
ORDER BY (properties->>'updated_at') DESC NULLS LAST,
         (properties->>'created_at') DESC NULLS LAST
LIMIT $2`

// VectorSearchWithCutoff is VectorSearch with an additional created_at filter for temporal scope.
//
// Args: $1 = vector string literal (text, cast to halfvec(1024)),
//
//	$2 = user_name (text),
//	$3 = memory_types (text[]),
//	$4 = limit (int),
//	$5 = cutoff ISO timestamp (text)
const VectorSearchWithCutoff = `
SELECT id::text,
       properties::text,
       1 - (embedding::halfvec(1024) <=> $1::halfvec(1024)) AS score,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = $2
  AND properties->>'memory_type' = ANY($3)
  AND ($6::text = '' OR properties->>'agent_id' = $6)
  AND embedding IS NOT NULL
  AND (properties->>'created_at') >= $5
ORDER BY embedding::halfvec(1024) <=> $1::halfvec(1024) ASC
LIMIT $4`

// FulltextSearchWithCutoff is FulltextSearch with an additional created_at filter for temporal scope.
//
// Args: $1 = tsquery string (text),
//
//	$2 = user_name (text),
//	$3 = memory_types (text[]),
//	$4 = limit (int),
//	$5 = cutoff ISO timestamp (text)
const FulltextSearchWithCutoff = `
SELECT id::text,
       properties::text,
       ts_rank(properties_tsvector_zh, to_tsquery('simple', $1)) AS rank
FROM %[1]s."Memory"
WHERE properties_tsvector_zh @@ to_tsquery('simple', $1)
  AND properties->>'status' = 'activated'
  AND properties->>'user_name' = $2
  AND properties->>'memory_type' = ANY($3)
  AND ($6::text = '' OR properties->>'agent_id' = $6)
  AND (properties->>'created_at') >= $5
ORDER BY rank DESC
LIMIT $4`

// GraphBFSTraversal performs a depth-limited BFS expansion from a set of seed memory node IDs.
// It follows the working_binding relationship encoded in properties->>'background' as
// "[working_binding:<ltm_id>]", expanding from LTM nodes found to their connected memories.
//
// The recursive CTE expands up to `depth` hops from the seeds. Results exclude the original
// seed IDs so the caller receives only newly discovered neighbor nodes.
//
// Args: $1 = seed_ids (text[]) — starting node IDs (typically the top-k vector hits),
//
//	$2 = user_name (text),
//	$3 = memory_types (text[]) — node types to include in traversal,
//	$4 = depth (int) — max BFS depth (recommended: 2),
//	$5 = limit (int) — max total results returned
const GraphBFSTraversal = `
WITH RECURSIVE bfs AS (
  -- Base case: seed nodes (matched by caller from vector/key/tag recall)
  SELECT id::text AS node_id, 0 AS depth
  FROM %[1]s."Memory"
  WHERE id = ANY($1::uuid[])
    AND properties->>'status' = 'activated'
    AND properties->>'user_name' = $2
    AND ($6::text = '' OR properties->>'agent_id' = $6)

  UNION ALL

  -- Recursive step: follow working_binding references up to depth hops
  SELECT m.id::text AS node_id, b.depth + 1
  FROM %[1]s."Memory" m
  JOIN bfs b ON (
    -- a) LTM node referenced by existing WM: "[working_binding:<ltm_id>]"
    m.id::text = substring(
      (SELECT properties->>'background' FROM %[1]s."Memory" WHERE id::text = b.node_id),
      '\[working_binding:([^\]]+)\]'
    )
    OR
    -- b) WM node that references the current LTM via working_binding in its own background
    substring(m.properties->>'background', '\[working_binding:([^\]]+)\]') = b.node_id
  )
  WHERE b.depth < $4
    AND m.properties->>'status' = 'activated'
    AND m.properties->>'user_name' = $2
    AND m.properties->>'memory_type' = ANY($3)
)
SELECT DISTINCT m.id::text, m.properties::text
FROM %[1]s."Memory" m
JOIN bfs ON m.id::text = bfs.node_id
WHERE m.properties->>'status' = 'activated'
  AND NOT (m.id = ANY($1::uuid[]))  -- exclude original seeds
  AND m.properties->>'memory_type' = ANY($3)
ORDER BY m.id::text
LIMIT $5`

