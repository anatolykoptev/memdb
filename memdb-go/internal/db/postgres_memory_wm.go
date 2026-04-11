package db

// postgres_memory_wm.go — WorkingMemory operations.
// Covers: count, oldest-first fetch, recent-with-embedding, cleanup.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// CountWorkingMemory returns the number of activated WorkingMemory nodes for a cube.
func (p *Postgres) CountWorkingMemory(ctx context.Context, userName string) (int64, error) {
	var count int64
	err := p.pool.QueryRow(ctx, fmt.Sprintf(queries.CountWorkingMemory, graphName), userName).Scan(&count)
	return count, err
}

// GetWorkingMemoryOldestFirst returns activated WorkingMemory nodes ordered oldest-first.
func (p *Postgres) GetWorkingMemoryOldestFirst(ctx context.Context, userName string, limit int) ([]MemNode, error) {
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.GetWorkingMemoryOldestFirst, graphName), userName, limit)
	if err != nil {
		return nil, fmt.Errorf("get wm oldest first: %w", err)
	}
	defer rows.Close()
	var nodes []MemNode
	for rows.Next() {
		var n MemNode
		if err := rows.Scan(&n.ID, &n.Text); err != nil {
			return nil, fmt.Errorf("get wm oldest first scan: %w", err)
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// GetRecentWorkingMemory returns the N most-recent activated WorkingMemory nodes with embeddings.
func (p *Postgres) GetRecentWorkingMemory(ctx context.Context, userName string, limit int) ([]WMNode, error) {
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.GetRecentWorkingMemory, graphName), userName, limit)
	if err != nil {
		return nil, fmt.Errorf("get recent working memory: %w", err)
	}
	defer rows.Close()
	var nodes []WMNode
	for rows.Next() {
		var n WMNode
		var embText string
		if err := rows.Scan(&n.ID, &n.Text, &n.TS, &embText); err != nil {
			return nil, fmt.Errorf("get recent wm scan: %w", err)
		}
		n.Embedding = ParseVectorString(embText)
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// CleanupWorkingMemory deletes oldest WorkingMemory nodes beyond keepLatest for a user.
func (p *Postgres) CleanupWorkingMemory(ctx context.Context, userName string, keepLatest int) (int64, error) {
	tag, err := p.pool.Exec(ctx, fmt.Sprintf(queries.CleanupOldestWorkingMemory, graphName), userName, keepLatest)
	if err != nil {
		return 0, fmt.Errorf("cleanup working memory: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CleanupWorkingMemoryWithIDs deletes oldest WorkingMemory nodes and returns deleted node UUIDs.
func (p *Postgres) CleanupWorkingMemoryWithIDs(ctx context.Context, userName string, keepLatest int) ([]string, error) {
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.CleanupOldestWorkingMemoryReturning, graphName), userName, keepLatest)
	if err != nil {
		return nil, fmt.Errorf("cleanup working memory with ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}
