package db

// postgres_memory_admin.go — admin and raw-memory detection operations.
// Covers: find/count raw conversation-window memories for reprocessing.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// FindRawMemories returns activated LTM/UserMemory nodes containing raw conversation window patterns.
func (p *Postgres) FindRawMemories(ctx context.Context, userName string, limit int) ([]RawMemory, error) {
	memTypes := []string{"LongTermMemory", "UserMemory"}
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.FindRawMemories, graphName), userName, memTypes, limit)
	if err != nil {
		return nil, fmt.Errorf("find raw memories: %w", err)
	}
	defer rows.Close()
	var results []RawMemory
	for rows.Next() {
		var r RawMemory
		if err := rows.Scan(&r.ID, &r.Memory); err != nil {
			return nil, fmt.Errorf("find raw memories scan: %w", err)
		}
		if r.ID != "" && r.Memory != "" {
			results = append(results, r)
		}
	}
	return results, rows.Err()
}

// CountRawMemories returns the total count of raw conversation-window memories for a user.
func (p *Postgres) CountRawMemories(ctx context.Context, userName string) (int64, error) {
	memTypes := []string{"LongTermMemory", "UserMemory"}
	var count int64
	err := p.pool.QueryRow(ctx, fmt.Sprintf(queries.CountRawMemories, graphName), userName, memTypes).Scan(&count)
	return count, err
}
