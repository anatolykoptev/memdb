package queries

// queries_memory_ltm.go — SQL queries for LongTermMemory vector search and dedup.
// Covers: LTM vector search, near-duplicate detection (full scan + ID-scoped).

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

// FindNearDuplicatesHNSW returns near-duplicate pairs using the HNSW index on the embedding column.
// For each activated memory a, it pulls top-K nearest neighbours via an indexed LATERAL scan,
// then filters by the cosine-similarity threshold. O(N·topK·log N) vs the O(N²) self-join
// in FindNearDuplicates. Approximate — recall depends on hnsw.ef_search (caller must SET LOCAL).
//
// The a.id < b.id predicate deduplicates symmetric pairs.
// %[1]s = graph schema name (e.g. memos_graph).
//
// Args: $1 = user_name (text), $2 = similarity threshold (float64), $3 = limit (int), $4 = per-node top-K (int)
const FindNearDuplicatesHNSW = `
SELECT
    a.id                    AS id_a,
    a.properties->>'memory' AS mem_a,
    b.id                    AS id_b,
    b.properties->>'memory' AS mem_b,
    1 - (a.embedding <=> b.embedding) AS score
FROM %[1]s."Memory" a
CROSS JOIN LATERAL (
    SELECT m.id, m.properties, m.embedding
    FROM %[1]s."Memory" m
    WHERE m.id > a.id
      AND m.properties->>'user_name' = $1
      AND m.properties->>'status' = 'activated'
      AND m.properties->>'memory_type' IN ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
      AND m.embedding IS NOT NULL
    ORDER BY m.embedding <=> a.embedding
    LIMIT $4
) b
WHERE a.properties->>'user_name' = $1
  AND a.properties->>'status' = 'activated'
  AND a.properties->>'memory_type' IN ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
  AND a.embedding IS NOT NULL
  AND 1 - (a.embedding <=> b.embedding) >= $2
ORDER BY score DESC
LIMIT $3`

// FindNearDuplicatesHNSWByIDs is the HNSW variant of FindNearDuplicatesByIDs.
// Restricted to pairs where at least one node is in $2 (text[] of memory IDs).
//
// Args: $1 = user_name (text), $2 = ids (text[]), $3 = similarity threshold (float64),
//
//	$4 = limit (int), $5 = per-node top-K (int)
const FindNearDuplicatesHNSWByIDs = `
SELECT
    a.id                    AS id_a,
    a.properties->>'memory' AS mem_a,
    b.id                    AS id_b,
    b.properties->>'memory' AS mem_b,
    1 - (a.embedding <=> b.embedding) AS score
FROM %[1]s."Memory" a
CROSS JOIN LATERAL (
    SELECT m.id, m.properties, m.embedding
    FROM %[1]s."Memory" m
    WHERE m.id > a.id
      AND m.properties->>'user_name' = $1
      AND m.properties->>'status' = 'activated'
      AND m.properties->>'memory_type' IN ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
      AND m.embedding IS NOT NULL
    ORDER BY m.embedding <=> a.embedding
    LIMIT $5
) b
WHERE a.properties->>'user_name' = $1
  AND a.properties->>'status' = 'activated'
  AND a.properties->>'memory_type' IN ('LongTermMemory', 'UserMemory', 'EpisodicMemory')
  AND a.embedding IS NOT NULL
  AND (a.id = ANY($2) OR b.id = ANY($2))
  AND 1 - (a.embedding <=> b.embedding) >= $3
ORDER BY score DESC
LIMIT $4`

// FindNearDuplicatesHNSWSQL returns the HNSW query string for testing.
func FindNearDuplicatesHNSWSQL() string { return FindNearDuplicatesHNSW }

// FindNearDuplicatesHNSWByIDsSQL returns the HNSW ByIDs query string for testing.
func FindNearDuplicatesHNSWByIDsSQL() string { return FindNearDuplicatesHNSWByIDs }
