package db

// postgres_entity.go — entity_nodes table operations.
// Covers: NormalizeEntityID, UpsertEntityNode, FindEntitiesByNormalizedID,
// GetMemoriesByEntityIDs.
//
// Table creation moved to migration 0006_entity_nodes.sql (applied by
// RunMigrations at startup).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// EntitySimilarityThreshold is the minimum cosine similarity (0–1) for two entity names
// to be considered the same real-world entity during identity resolution.
// Above this threshold, the new name is merged into the existing entity node.
const EntitySimilarityThreshold = 0.92

// NormalizeEntityID returns a stable, lowercase identifier for an entity name.
// Used as the primary key in entity_nodes to enable identity resolution.
func NormalizeEntityID(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// UpsertEntityNode inserts or updates an entity node scoped to a user.
// Returns the normalized entity ID. Non-fatal caller pattern: log and continue.
func (p *Postgres) UpsertEntityNode(ctx context.Context, name, entityType, userName, now string) (string, error) {
	id := NormalizeEntityID(name)
	if id == "" {
		return "", errors.New("entity name is empty")
	}
	_, err := p.pool.Exec(ctx, queries.UpsertEntityNode, id, userName, name, entityType, now, now)
	if err != nil {
		return "", fmt.Errorf("upsert entity node %q: %w", id, err)
	}
	return id, nil
}

// UpsertEntityNodeWithEmbedding is the embedding-aware variant of UpsertEntityNode.
// Before inserting, it searches for an existing entity node with cosine similarity
// above EntitySimilarityThreshold. If found, it merges into that node (returns its ID).
// This resolves aliases like "Яндекс" vs "Yandex" without an extra LLM call.
// Falls back to plain UpsertEntityNode if embedding is empty.
func (p *Postgres) UpsertEntityNodeWithEmbedding(ctx context.Context, name, entityType, userName, now, embVec string) (string, error) {
	if embVec == "" {
		return p.UpsertEntityNode(ctx, name, entityType, userName, now)
	}
	id := NormalizeEntityID(name)
	if id == "" {
		return "", errors.New("entity name is empty")
	}
	// Step 1: look for an existing entity node with high cosine similarity.
	const findSimilar = `
SELECT id
FROM entity_nodes
WHERE user_name = $1
  AND embedding IS NOT NULL
  AND 1 - (embedding <=> $2::halfvec(1024)) >= $3
ORDER BY embedding <=> $2::halfvec(1024) ASC
LIMIT 1`
	var existingID string
	err := p.pool.QueryRow(ctx, findSimilar, userName, embVec, EntitySimilarityThreshold).Scan(&existingID)
	if err == nil && existingID != "" {
		// Merge: update name/type/updated_at on the existing node and return its ID.
		const updateNode = `
UPDATE entity_nodes SET name = $3, entity_type = $4, updated_at = $5
WHERE id = $1 AND user_name = $2`
		_, _ = p.pool.Exec(ctx, updateNode, existingID, userName, name, entityType, now)
		return existingID, nil
	}
	// Step 2: no similar entity found — insert new node with embedding.
	const insertWithEmb = `
INSERT INTO entity_nodes (id, user_name, name, entity_type, created_at, updated_at, embedding)
VALUES ($1, $2, $3, $4, $5, $6, $7::halfvec(1024))
ON CONFLICT (id, user_name) DO UPDATE
SET name        = EXCLUDED.name,
    entity_type = EXCLUDED.entity_type,
    updated_at  = EXCLUDED.updated_at,
    embedding   = EXCLUDED.embedding`
	_, err = p.pool.Exec(ctx, insertWithEmb, id, userName, name, entityType, now, now, embVec)
	if err != nil {
		return "", fmt.Errorf("upsert entity node with embedding %q: %w", id, err)
	}
	return id, nil
}

// FindEntitiesByNormalizedID returns entity IDs that match any of the given
// normalized names for a user. Used by SearchService for entity-graph recall.
func (p *Postgres) FindEntitiesByNormalizedID(ctx context.Context, normalizedIDs []string, cubeID, personID string) ([]string, error) {
	if len(normalizedIDs) == 0 {
		return nil, nil
	}
	rows, err := p.pool.Query(ctx, queries.FindEntitiesByNormalizedID, cubeID, normalizedIDs)
	if err != nil {
		return nil, fmt.Errorf("find entities by id: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetMemoriesByEntityIDs returns activated memory nodes that mention any of the
// given entity IDs via MENTIONS_ENTITY edges. Used for entity-graph recall in search.
func (p *Postgres) GetMemoriesByEntityIDs(ctx context.Context, entityIDs []string, cubeID, personID string, limit int) ([]GraphRecallResult, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.GetMemoriesByEntityIDs, graphName)
	rows, err := p.pool.Query(ctx, q, cubeID, personID, entityIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("get memories by entity ids: %w", err)
	}
	defer rows.Close()

	var results []GraphRecallResult
	for rows.Next() {
		var r GraphRecallResult
		if err := rows.Scan(&r.ID, &r.Properties); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
