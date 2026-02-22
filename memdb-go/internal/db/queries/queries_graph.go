package queries

// queries_graph.go — SQL queries for graph edges and importance lifecycle.
// Covers: memory_edges DDL, InsertMemoryEdge, GraphRecallByEdge,
//         FilterExistingContentHashes, IncrRetrievalCount,
//         DecayImportanceScores, AutoArchiveLowImportance.

// --- Graph Edges (memory_edges table) ---
//
// memory_edges is a plain PostgreSQL table (not AGE Cypher) that stores
// directed relationships between memory nodes identified by their property UUID.
// Created lazily via EnsureEdgesTable on first use.

// CreateEdgesTable is the reference DDL for the memory_edges table.
// Actual table creation is done via EnsureEdgesTable (which also runs ALTER TABLE migrations).
const CreateEdgesTable = `
CREATE TABLE IF NOT EXISTS memory_edges (
    from_id    TEXT NOT NULL,
    to_id      TEXT NOT NULL,
    relation   TEXT NOT NULL,
    created_at TEXT,
    valid_at   TEXT,
    invalid_at TEXT,
    PRIMARY KEY (from_id, to_id, relation)
);
CREATE INDEX IF NOT EXISTS memory_edges_to_idx ON memory_edges(to_id, relation)`

// InsertMemoryEdge inserts a directed edge between two memory nodes (idempotent).
// Args: $1 = from_id (text), $2 = to_id (text), $3 = relation (text),
//       $4 = created_at (text), $5 = valid_at (text, empty string = NULL)
const InsertMemoryEdge = `
INSERT INTO memory_edges (from_id, to_id, relation, created_at, valid_at)
VALUES ($1, $2, $3, $4, NULLIF($5, ''))
ON CONFLICT (from_id, to_id, relation) DO NOTHING`

// GraphRecallByEdge returns memory nodes reachable from seed_ids via edges of a given relation.
// Used to traverse EXTRACTED_FROM, MERGED_INTO, and CONTRADICTS relationships in graph recall.
// Bi-temporal filter: only follows edges where invalid_at IS NULL (currently valid edges).
// Args: $1 = seed_ids (text[]), $2 = relation (text), $3 = user_name (text), $4 = limit (int)
const GraphRecallByEdge = `
SELECT m.id::text,
       (m.properties - 'sources')::text
FROM %[1]s."Memory" m
JOIN memory_edges e ON m.id = e.to_id
WHERE e.from_id = ANY($1)
  AND e.relation = $2
  AND e.invalid_at IS NULL
  AND m.properties->>'user_name' = $3
  AND m.properties->>'status' = 'activated'
LIMIT $4`

// FilterExistingContentHashes returns content_hash values that already exist for a user.
// Used to batch-deduplicate before insert without per-item DB round-trips.
// Args: $1 = hashes (text[]), $2 = user_name (text)
const FilterExistingContentHashes = `
SELECT properties->'info'->>'content_hash' AS hash
FROM %[1]s."Memory"
WHERE properties->'info'->>'content_hash' = ANY($1)
  AND properties->>'user_name' = $2
  AND properties->>'status' = 'activated'`

// IncrRetrievalCount increments retrieval_count and boosts importance_score for a batch of nodes.
// Called asynchronously after search to track memory usage frequency.
// importance_score is capped at 2.0 (prevents unbounded growth for very popular memories).
// Args: $1 = ids (text[]), $2 = now (text, ISO timestamp)
const IncrRetrievalCount = `
UPDATE %[1]s."Memory"
SET properties = properties
    || jsonb_build_object(
        'retrieval_count',   COALESCE((properties->>'retrieval_count')::int, 0) + 1,
        'last_retrieved_at', $2::text,
        'importance_score',  LEAST(2.0, COALESCE((properties->>'importance_score')::float, 1.0) + 0.1)
    )
WHERE id = ANY($1)
  AND properties->>'status' = 'activated'`

// DecayImportanceScores multiplies importance_score by 0.95 for all LTM/UserMemory nodes of a user.
// Called periodically (e.g. every 6h) to cause infrequently-retrieved memories to fade.
// Args: $1 = user_name (text)
const DecayImportanceScores = `
UPDATE %[1]s."Memory"
SET properties = properties
    || jsonb_build_object(
        'importance_score', GREATEST(0.0,
            COALESCE((properties->>'importance_score')::float, 1.0) * 0.95)
    )
WHERE properties->>'user_name' = $1
  AND properties->>'status' = 'activated'
  AND properties->>'memory_type' IN ('LongTermMemory', 'UserMemory')`

// AutoArchiveLowImportance marks memories with importance_score below the threshold as archived.
// Only affects nodes that explicitly have an importance_score set (field exists in properties).
// Args: $1 = user_name (text), $2 = threshold (float), $3 = now (text, ISO timestamp)
const AutoArchiveLowImportance = `
UPDATE %[1]s."Memory"
SET properties = properties
    || jsonb_build_object('status', 'archived', 'updated_at', $3::text)
WHERE properties->>'user_name' = $1
  AND properties->>'status' = 'activated'
  AND properties->>'memory_type' IN ('LongTermMemory', 'UserMemory')
  AND (properties ? 'importance_score')
  AND (properties->>'importance_score')::float < $2`
