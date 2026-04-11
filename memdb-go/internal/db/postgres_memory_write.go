package db

// postgres_memory_write.go — memory node write operations.
// Covers: delete, update content, update full (props+embedding), delete-all.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// DeleteByPropertyIDs deletes nodes matching the given property IDs and user name.
func (p *Postgres) DeleteByPropertyIDs(ctx context.Context, propertyIDs []string, userName string) (int64, error) {
	tag, err := p.pool.Exec(ctx, fmt.Sprintf(queries.DeleteByPropertyIDs, graphName), propertyIDs, userName)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// UpdateMemoryContent updates the memory text for a given memory node.
func (p *Postgres) UpdateMemoryContent(ctx context.Context, memoryID, content string) (bool, error) {
	tag, err := p.pool.Exec(ctx, fmt.Sprintf(queries.UpdateMemoryContent, graphName), memoryID, content)
	if err != nil {
		return false, fmt.Errorf("update memory: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteAllByUser deletes all activated memories for a user.
func (p *Postgres) DeleteAllByUser(ctx context.Context, userName string) (int64, error) {
	tag, err := p.pool.Exec(ctx, fmt.Sprintf(queries.DeleteAllByUser, graphName), userName)
	if err != nil {
		return 0, fmt.Errorf("delete all by user: %w", err)
	}
	return tag.RowsAffected(), nil
}

// UpdateMemoryByID atomically replaces properties + embedding for a memory node.
func (p *Postgres) UpdateMemoryByID(ctx context.Context, memoryID, cubeID string, propsJSON []byte, embedding string) error {
	var returnedID string
	err := p.pool.QueryRow(ctx, fmt.Sprintf(queries.UpdateMemoryPropsAndEmbedding, graphName), memoryID, cubeID, propsJSON, embedding).Scan(&returnedID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("memory not found: id=%s cube=%s", memoryID, cubeID)
		}
		return fmt.Errorf("update memory: %w", err)
	}
	return nil
}
