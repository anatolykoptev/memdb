package queries

// queries_entity.go — SQL queries for entity_nodes table.
// Covers: DDL, UpsertEntityNode, FindEntitiesByNormalizedID, GetMemoriesByEntityIDs.

// --- Entity Nodes (entity_nodes table) ---
//
// entity_nodes stores named entities extracted from memory facts.
// Each entity is scoped to a user (cube) and identified by a normalized ID
// (lowercase trimmed name). This enables entity-level graph recall:
// finding all memories that mention a given person, org, or concept.

// CreateEntityNodesTable is the reference DDL for the entity_nodes table.
// Actual table creation is done via EnsureEntityNodesTable (which also runs ALTER TABLE migrations
// to add the embedding halfvec(1024) column and HNSW index for existing tables).
const CreateEntityNodesTable = `
CREATE TABLE IF NOT EXISTS entity_nodes (
    id          TEXT NOT NULL,
    user_name   TEXT NOT NULL,
    name        TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    created_at  TEXT,
    updated_at  TEXT,
    embedding   halfvec(1024),
    PRIMARY KEY (id, user_name)
);
CREATE INDEX IF NOT EXISTS entity_nodes_user_idx ON entity_nodes(user_name);
CREATE INDEX IF NOT EXISTS entity_nodes_name_idx ON entity_nodes(user_name, id);
CREATE INDEX IF NOT EXISTS entity_nodes_emb_idx ON entity_nodes USING hnsw (embedding halfvec_cosine_ops)`

// UpsertEntityNode inserts or updates an entity node (idempotent by normalized id + user).
// Args: $1 = id (text), $2 = user_name (text), $3 = name (text), $4 = entity_type (text),
//
//	$5 = created_at (text), $6 = updated_at (text)
const UpsertEntityNode = `
INSERT INTO entity_nodes (id, user_name, name, entity_type, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id, user_name) DO UPDATE
SET name        = EXCLUDED.name,
    entity_type = EXCLUDED.entity_type,
    updated_at  = EXCLUDED.updated_at`

// FindEntitiesByNormalizedID returns entity node IDs that match any of the given
// normalized names for a user. Used to look up entities by query tokens.
// Args: $1 = user_name (text), $2 = normalized_ids (text[])
const FindEntitiesByNormalizedID = `
SELECT id
FROM entity_nodes
WHERE user_name = $1
  AND id = ANY($2)`

// GetMemoriesByEntityIDs returns activated memory nodes that have a MENTIONS_ENTITY
// edge pointing to any of the given entity IDs for a user.
// Bi-temporal filter: only returns edges where invalid_at IS NULL (currently valid facts).
// Invalidated edges (from deleted/superseded memories) are excluded from recall.
// Args: $1 = user_name (text), $2 = user_id (text), $3 = entity_ids (text[]), $4 = limit (int)
const GetMemoriesByEntityIDs = `
SELECT m.id::text,
       (m.properties::text::jsonb - 'sources')::text
FROM %[1]s."Memory" m
JOIN memory_edges e ON m.id::text = e.from_id
WHERE e.to_id = ANY($3)
  AND e.relation = 'MENTIONS_ENTITY'
  AND e.invalid_at IS NULL
  AND m.properties->>(('user_name'::text)) = $1
  AND m.properties->>(('user_id'::text))   = $2
  AND m.properties->>(('status'::text)) = 'activated'
LIMIT $4`
