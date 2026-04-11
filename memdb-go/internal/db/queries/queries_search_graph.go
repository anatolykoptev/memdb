package queries

// queries_search_graph.go — graph recall and working-memory SQL constants used by search.
// Covers: GraphRecallByKey, GraphRecallByTags, GetWorkingMemory, GraphBFSTraversal.

// GraphRecallByKey finds memory nodes whose properties->>'key' matches any of the given tokens.
// Returns id, properties and a fixed score (no vector similarity).
//
// Args: $1 = user_name (text),
//
//	$2 = user_id (text),
//	$3 = memory_types (text[]),
//	$4 = keys (text[]),
//	$5 = limit (int),
//	$6 = agent_id (text, '' for any)
const GraphRecallByKey = `
SELECT id::text,
       (properties - 'sources')::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = $1
  AND properties->>'user_id'   = $2
  AND properties->>'memory_type' = ANY($3)
  AND properties->>'key' = ANY($4)
  AND ($6::text = '' OR properties->>'agent_id' = $6)
LIMIT $5`

// GraphRecallByTags finds memory nodes whose tags array overlaps with given tags.
// Uses JSONB containment: for each candidate tag, checks if the tags array contains it.
// Post-filter in Go ensures overlap >= 2.
//
// Args: $1 = user_name (text),
//
//	$2 = user_id (text),
//	$3 = memory_types (text[]),
//	$4 = tags (text[]) — candidate tags to check overlap,
//	$5 = limit (int),
//	$6 = agent_id (text, '' for any)
const GraphRecallByTags = `
SELECT id::text,
       (properties - 'sources')::text,
       tag_overlap
FROM (
    SELECT id, properties,
           array_length(
               ARRAY(
                   SELECT unnest(ARRAY(SELECT jsonb_array_elements_text(properties->'tags')))
                   INTERSECT
                   SELECT unnest($4::text[])
               ), 1
           ) AS tag_overlap
    FROM %[1]s."Memory"
    WHERE properties->>'status' = 'activated'
      AND properties->>'user_name' = $1
      AND properties->>'user_id'   = $2
      AND properties->>'memory_type' = ANY($3)
      AND ($6::text = '' OR properties->>'agent_id' = $6)
      AND properties->'tags' IS NOT NULL
      AND jsonb_array_length(properties->'tags') > 0
) sub
WHERE tag_overlap >= 2
ORDER BY tag_overlap DESC
LIMIT $5`

// GetWorkingMemory returns all activated WorkingMemory items for a user.
// No vector/fulltext search — just a filtered SELECT ordered by recency.
// Returns embeddings so callers can compute cosine similarity vs query.
//
// Args: $1 = user_name (text),
//
//	$2 = user_id (text),
//	$3 = limit (int),
//	$4 = agent_id (text, '' for any)
const GetWorkingMemory = `
SELECT id::text,
       (properties - 'sources')::text,
       embedding::text
FROM %[1]s."Memory"
WHERE properties->>'status' = 'activated'
  AND properties->>'user_name' = $1
  AND properties->>'user_id'   = $2
  AND properties->>'memory_type' = 'WorkingMemory'
  AND ($4::text = '' OR properties->>'agent_id' = $4)
  AND embedding IS NOT NULL
ORDER BY (properties->>'updated_at') DESC NULLS LAST,
         (properties->>'created_at') DESC NULLS LAST
LIMIT $3`

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
//	$3 = user_id (text),
//	$4 = memory_types (text[]) — node types to include in traversal,
//	$5 = depth (int) — max BFS depth (recommended: 2),
//	$6 = limit (int) — max total results returned,
//	$7 = agent_id (text, '' for any)
const GraphBFSTraversal = `
WITH RECURSIVE bfs AS (
  -- Base case: seed nodes (matched by caller from vector/key/tag recall)
  SELECT id::text AS node_id, 0 AS depth
  FROM %[1]s."Memory"
  WHERE id = ANY($1::text[])
    AND properties->>'status' = 'activated'
    AND properties->>'user_name' = $2
    AND properties->>'user_id'   = $3
    AND ($7::text = '' OR properties->>'agent_id' = $7)

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
  WHERE b.depth < $5
    AND m.properties->>'status' = 'activated'
    AND m.properties->>'user_name' = $2
    AND m.properties->>'user_id'   = $3
    AND m.properties->>'memory_type' = ANY($4)
)
SELECT DISTINCT m.id::text, (m.properties - 'sources')::text
FROM %[1]s."Memory" m
JOIN bfs ON m.id::text = bfs.node_id
WHERE m.properties->>'status' = 'activated'
  AND NOT (m.id = ANY($1::text[]))  -- exclude original seeds
  AND m.properties->>'memory_type' = ANY($4)
ORDER BY m.id::text
LIMIT $6`
