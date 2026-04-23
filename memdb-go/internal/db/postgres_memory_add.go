package db

// postgres_memory_add.go — native add pipeline operations.
// Covers: batch insert (upsert), full-node update, content-hash check.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// InsertMemoryNodes inserts multiple memory nodes in a single transaction (DELETE+INSERT per node).
func (p *Postgres) InsertMemoryNodes(ctx context.Context, nodes []MemoryInsertNode) error {
	if len(nodes) == 0 {
		return nil
	}

	delQ := fmt.Sprintf(queries.DeleteMemoryByPropID, graphName)
	insQ := fmt.Sprintf(queries.InsertMemoryNode, graphName)

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, n := range nodes {
		batch.Queue(delQ, n.ID)
		batch.Queue(insQ, n.ID, n.PropertiesJSON, n.EmbeddingVec)
	}

	br := tx.SendBatch(ctx, batch)
	for range nodes {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return fmt.Errorf("delete before insert: %w", err)
		}
		if _, err := br.Exec(); err != nil {
			br.Close()
			return fmt.Errorf("insert node: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("close batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit memory insert: %w", err)
	}
	for _, n := range nodes {
		memType, cubeID := extractMemoryLabels(n.PropertiesJSON)
		dbMx().MemoryAdded.Add(ctx, 1,
			metric.WithAttributes(
				attribute.String("type", memType),
				attribute.String("cube_id", cubeID),
			),
		)
	}
	return nil
}

// nodeMetricProps holds only the fields needed for metric labels.
type nodeMetricProps struct {
	MemoryType string `json:"memory_type"`
	UserName   string `json:"user_name"`
}

// extractMemoryLabels parses memory_type and user_name from PropertiesJSON for low-cardinality metric labels.
// Returns empty strings on parse failure — metric is still emitted, just unlabelled.
func extractMemoryLabels(propsJSON []byte) (memType, cubeID string) {
	var p nodeMetricProps
	if err := json.Unmarshal(propsJSON, &p); err != nil {
		return "", ""
	}
	return p.MemoryType, p.UserName
}

// UpdateMemoryNodeFull updates memory text, embedding, and updated_at of an existing node.
func (p *Postgres) UpdateMemoryNodeFull(ctx context.Context, memoryID, newText, embeddingVec, updatedAt string) error {
	_, err := p.pool.Exec(ctx, fmt.Sprintf(queries.UpdateMemoryNodeFull, graphName), memoryID, newText, embeddingVec, updatedAt)
	if err != nil {
		return fmt.Errorf("update memory node: %w", err)
	}
	return nil
}

// CheckContentHashExists checks whether an activated memory with the given content_hash exists for a user.
func (p *Postgres) CheckContentHashExists(ctx context.Context, hash, userName string) (bool, error) {
	var exists bool
	err := p.pool.QueryRow(ctx, fmt.Sprintf(queries.CheckContentHashExists, graphName), hash, userName).Scan(&exists)
	return exists, err
}
