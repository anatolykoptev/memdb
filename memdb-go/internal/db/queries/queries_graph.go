package queries

// queries_graph.go — SQL queries for graph edges and importance lifecycle.
// Covers: memory_edges DDL, InsertMemoryEdge, GraphRecallByEdge,
//         FilterExistingContentHashes, IncrRetrievalCount,
//         DecayImportanceScores, AutoArchiveLowImportance.

// --- Graph Edges (memory_edges table) ---
//
// memory_edges is a plain PostgreSQL table (not AGE Cypher) that stores
// directed relationships between memory nodes identified by their property UUID.
// Created by migration 0005_memory_edges.sql at startup.

// InsertMemoryEdge inserts a directed edge between two memory nodes (idempotent).
// Args: $1 = from_id (text), $2 = to_id (text), $3 = relation (text),
//
//	$4 = created_at (text), $5 = valid_at (text, empty string = NULL)
const InsertMemoryEdge = `
INSERT INTO memory_edges (from_id, to_id, relation, created_at, valid_at)
VALUES ($1, $2, $3, $4, NULLIF($5, ''))
ON CONFLICT (from_id, to_id, relation) DO NOTHING`

// InsertMemoryEdgeWithConfidence is the D3 variant that stores the relation
// detector's confidence score + rationale. Same PK conflict resolution as
// InsertMemoryEdge — first insert wins so retries stay idempotent.
// Args: $1 = from_id, $2 = to_id, $3 = relation, $4 = created_at,
//
//	$5 = valid_at (empty string = NULL), $6 = confidence (0..1), $7 = rationale
const InsertMemoryEdgeWithConfidence = `
INSERT INTO memory_edges (from_id, to_id, relation, created_at, valid_at, confidence, rationale)
VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, NULLIF($7, ''))
ON CONFLICT (from_id, to_id, relation) DO NOTHING`

// BulkInsertMemoryEdges inserts many edges in a single round-trip via
// UNNEST. Idempotent: duplicate (from_id, to_id, relation) rows skip.
// Used by the M8 structural-edge emitter at ingest (SAME_SESSION,
// TIMELINE_NEXT, SIMILAR_COSINE_HIGH) — three round-trips per /add otherwise.
//
// Args (all parallel arrays of equal length):
//
//	$1 = from_ids   (text[])
//	$2 = to_ids     (text[])
//	$3 = relations  (text[])
//	$4 = created_at (text)        — single timestamp shared across the batch
//	$5 = confidence (float8[])    — weight in [0,1]
//	$6 = rationales (text[])      — opaque payload (e.g. {"dt_seconds":42})
const BulkInsertMemoryEdges = `
INSERT INTO memory_edges (from_id, to_id, relation, created_at, confidence, rationale)
SELECT f, t, r, $4, c, NULLIF(rat, '')
FROM UNNEST($1::text[], $2::text[], $3::text[], $5::float8[], $6::text[])
     AS u(f, t, r, c, rat)
ON CONFLICT (from_id, to_id, relation) DO NOTHING`

// SessionMemoryNeighborsRecent returns the most-recent activated
// LongTermMemory/UserMemory rows in (cube_id, session_id), sorted DESC so the
// first row is the immediate predecessor of the memory being ingested.
//
// Use this for SAME_SESSION / TIMELINE_NEXT structural edges — recent context
// is relevant context; fetching the OLDEST rows anchors TIMELINE_NEXT 160
// turns in the past on a 200-message session (the failure mode SessionMemory
// Neighbors ASC had).
//
// memory_type whitelist intentionally excludes WorkingMemory: WM rows are
// transient (deleted by the WM→LTM transfer worker), so edges pointing at
// them would dangle.
//
// Embedding is returned as text for ParseVectorString — keeps this query
// reusable for SIMILAR_COSINE_HIGH edge candidates without a second round-trip.
//
// Args: $1 = cube_id (user_name), $2 = session_id, $3 = limit (int)
const SessionMemoryNeighborsRecent = `
SELECT
    properties->>(('id'::text))         AS memory_id,
    COALESCE(properties->>(('created_at'::text)), '') AS created_at,
    COALESCE(embedding::text, '')       AS embedding_text
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text))   = $1
  AND properties->>(('session_id'::text))  = $2
  AND properties->>(('status'::text))      = 'activated'
  AND properties->>(('memory_type'::text)) IN ('LongTermMemory', 'UserMemory')
ORDER BY properties->>(('created_at'::text)) DESC
LIMIT $3`

// SessionMemoryNeighbors is retained for backwards compatibility only.
// New code MUST use SessionMemoryNeighborsRecent (ORDER BY DESC) — see
// the bug description above for why ASC breaks TIMELINE_NEXT on long sessions.
//
// Deprecated: use SessionMemoryNeighborsRecent.
const SessionMemoryNeighbors = `
SELECT
    properties->>(('id'::text))         AS memory_id,
    COALESCE(properties->>(('created_at'::text)), '') AS created_at,
    COALESCE(embedding::text, '')       AS embedding_text
FROM %[1]s."Memory"
WHERE properties->>(('user_name'::text))   = $1
  AND properties->>(('session_id'::text))  = $2
  AND properties->>(('status'::text))      = 'activated'
  AND properties->>(('memory_type'::text)) IN ('LongTermMemory', 'UserMemory')
ORDER BY properties->>(('created_at'::text)) ASC
LIMIT $3`

// GraphRecallByEdge returns memory nodes reachable from seed_ids via edges of a given relation.
// Used to traverse EXTRACTED_FROM, MERGED_INTO, and CONTRADICTS relationships in graph recall.
// Bi-temporal filter: only follows edges where invalid_at IS NULL (currently valid edges).
// Args: $1 = seed_ids (text[]), $2 = relation (text), $3 = user_name (text),
//
//	$4 = user_id (text), $5 = limit (int)
// Returned id is the stable property UUID (properties->>'id'), matching
// memory_edges.from_id / to_id which also store property UUIDs.
const GraphRecallByEdge = `
SELECT m.properties->>(('id'::text)) AS memory_id,
       (m.properties::text::jsonb - 'sources')::text
FROM %[1]s."Memory" m
JOIN memory_edges e ON m.properties->>(('id'::text)) = e.to_id
WHERE e.from_id = ANY($1)
  AND e.relation = $2
  AND e.invalid_at IS NULL
  AND m.properties->>(('user_name'::text)) = $3
  AND m.properties->>(('user_id'::text))   = $4
  AND m.properties->>(('status'::text)) = 'activated'
LIMIT $5`

// FilterExistingContentHashes returns content_hash values that already exist for a user.
// Used to batch-deduplicate before insert without per-item DB round-trips.
// Args: $1 = hashes (text[]), $2 = user_name (text)
const FilterExistingContentHashes = `
SELECT properties->('info'::text)->>(('content_hash'::text)) AS hash
FROM %[1]s."Memory"
WHERE properties->('info'::text)->>(('content_hash'::text)) = ANY($1)
  AND properties->>(('user_name'::text)) = $2
  AND properties->>(('status'::text)) = 'activated'`

// IncrRetrievalCount increments retrieval_count and boosts importance_score for a batch of nodes.
// Called asynchronously after search to track memory usage frequency.
// importance_score is capped at 2.0 (prevents unbounded growth for very popular memories).
//
// D1 additions (2026-04-24):
//   - access_count — property-level counter used by the D1 importance
//     multiplier in rerank (1 + log(1+n), capped at 5).
//   - last_accessed_at — reflects actual usage recency, consumed by
//     resolveRefTimestamp as the highest-priority decay reference.
//
// Args: $1 = ids (text[]) — property UUIDs (properties->>'id'), NOT AGE graphids,
//
//	$2 = now (text, ISO timestamp)
const IncrRetrievalCount = `
UPDATE %[1]s."Memory"
SET properties = (
        (properties::text::jsonb || jsonb_build_object(
            'retrieval_count',   COALESCE((properties->>(('retrieval_count'::text)))::int, 0) + 1,
            'access_count',      COALESCE((properties->>(('access_count'::text)))::int, 0) + 1,
            'last_retrieved_at', $2::text,
            'last_accessed_at',  $2::text,
            'importance_score',  LEAST(2.0, COALESCE((properties->>(('importance_score'::text)))::float, 1.0) + 0.1)
        ))::text
    )::agtype
WHERE properties->>(('id'::text)) = ANY($1)
  AND properties->>(('status'::text)) = 'activated'`

// DecayImportanceScores multiplies importance_score by 0.95 for all LTM/UserMemory nodes of a user.
// Called periodically (e.g. every 6h) to cause infrequently-retrieved memories to fade.
// Args: $1 = user_name (text)
const DecayImportanceScores = `
UPDATE %[1]s."Memory"
SET properties = (
        (properties::text::jsonb || jsonb_build_object(
            'importance_score', GREATEST(0.0,
                COALESCE((properties->>(('importance_score'::text)))::float, 1.0) * 0.95)
        ))::text
    )::agtype
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('status'::text)) = 'activated'
  AND properties->>(('memory_type'::text)) IN ('LongTermMemory', 'UserMemory')`

// AutoArchiveLowImportance marks memories with importance_score below the threshold as archived.
// Only affects nodes that explicitly have an importance_score set (field exists in properties).
// Args: $1 = user_name (text), $2 = threshold (float), $3 = now (text, ISO timestamp)
const AutoArchiveLowImportance = `
UPDATE %[1]s."Memory"
SET properties = (
        (properties::text::jsonb || jsonb_build_object('status', 'archived', 'updated_at', $3::text))::text
    )::agtype
WHERE properties->>(('user_name'::text)) = $1
  AND properties->>(('status'::text)) = 'activated'
  AND properties->>(('memory_type'::text)) IN ('LongTermMemory', 'UserMemory')
  AND (properties ? ('importance_score'::text))
  AND (properties->>(('importance_score'::text)))::float < $2`
