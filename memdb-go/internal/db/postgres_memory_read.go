package db

// postgres_memory_read.go — memory node read operations.
// Covers: get by id(s), paginated list, filter-based get, user-name mapping.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// GetMemoryByPropertyID retrieves a single memory node by its property UUID.
// Returns nil if not found. Used by GET /product/get_memory/{memory_id}.
func (p *Postgres) GetMemoryByPropertyID(ctx context.Context, propertyID string) (map[string]any, error) {
	q := fmt.Sprintf(queries.GetMemoryByPropertyID, graphName)
	var id, propsStr string
	err := p.pool.QueryRow(ctx, q, propertyID).Scan(&id, &propsStr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return map[string]any{"memory_id": id, "properties": propsStr}, nil
}

// GetMemoriesByPropertyIDs retrieves full memory nodes by property UUID (no user_name filter).
func (p *Postgres) GetMemoriesByPropertyIDs(ctx context.Context, ids []string) ([]map[string]any, error) {
	q := fmt.Sprintf(queries.GetMemoriesByPropertyIDs, graphName)
	rows, err := p.pool.Query(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var id, propsStr string
		if err := rows.Scan(&id, &propsStr); err != nil {
			return nil, err
		}
		results = append(results, map[string]any{"memory_id": id, "properties": propsStr})
	}
	return results, rows.Err()
}

// GetMemoryByPropertyIDs retrieves memory text nodes scoped to a user.
func (p *Postgres) GetMemoryByPropertyIDs(ctx context.Context, ids []string, userName string) ([]MemNode, error) {
	q := fmt.Sprintf(queries.GetMemoryByPropertyIDs, graphName)
	rows, err := p.pool.Query(ctx, q, ids, userName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []MemNode
	for rows.Next() {
		var n MemNode
		if err := rows.Scan(&n.ID, &n.Text); err != nil {
			return nil, err
		}
		results = append(results, n)
	}
	return results, rows.Err()
}

// MemoryKeyListItem is the per-row payload for ListMemoriesByKeyPrefix.
// Char size avoids returning full memory text for listing requests.
type MemoryKeyListItem struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	MemoryType string `json:"memory_type"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	CharSize   int    `json:"char_size"`
}

// GetMemoryByKey retrieves a single activated memory addressed by
// (cubeID, userID, key). Returns nil if not found. Used by
// POST /product/get_memory_by_key.
func (p *Postgres) GetMemoryByKey(ctx context.Context, cubeID, userID, key string) (map[string]any, error) {
	q := fmt.Sprintf(queries.GetMemoryByKey, graphName)
	var id, propsStr string
	err := p.pool.QueryRow(ctx, q, cubeID, userID, key).Scan(&id, &propsStr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return map[string]any{"memory_id": id, "properties": propsStr}, nil
}

// ListMemoriesByKeyPrefix returns activated memories for (cubeID, userID)
// whose key starts with prefix. Limit/offset enforced by the caller; this
// method passes them through verbatim. Uses a LIKE query backed by the
// trigram GIN index on properties.key (migration 0018).
func (p *Postgres) ListMemoriesByKeyPrefix(ctx context.Context, cubeID, userID, prefix string, limit, offset int) ([]MemoryKeyListItem, error) {
	q := fmt.Sprintf(queries.ListMemoriesByKeyPrefix, graphName)
	pattern := prefix + "%"
	rows, err := p.pool.Query(ctx, q, cubeID, userID, pattern, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MemoryKeyListItem
	for rows.Next() {
		var it MemoryKeyListItem
		if err := rows.Scan(&it.ID, &it.Key, &it.MemoryType, &it.CreatedAt, &it.UpdatedAt, &it.CharSize); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// scanPaginatedMemoryRows scans id+propsJSON pairs from a pgx rows result set.
func scanPaginatedMemoryRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}) ([]map[string]any, error) {
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var id string
		var propsJSON []byte
		if err := rows.Scan(&id, &propsJSON); err != nil {
			return nil, err
		}
		results = append(results, map[string]any{"memory_id": id, "properties": string(propsJSON)})
	}
	return results, rows.Err()
}

// GetAllMemoriesByTypes returns paginated memories for a user across multiple memory types.
func (p *Postgres) GetAllMemoriesByTypes(ctx context.Context, userName string, memoryTypes []string, page, pageSize int) ([]map[string]any, int, error) {
	offset := page * pageSize
	var total int
	if err := p.pool.QueryRow(ctx, fmt.Sprintf(queries.CountByUserAndTypes, graphName), userName, memoryTypes).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.GetAllMemoriesByTypes, graphName), userName, memoryTypes, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	results, err := scanPaginatedMemoryRows(rows)
	return results, total, err
}

// GetAllMemories returns paginated memories for a user filtered by memory_type.
func (p *Postgres) GetAllMemories(ctx context.Context, userName, memoryType string, page, pageSize int) ([]map[string]any, int, error) {
	offset := page * pageSize
	var total int
	if err := p.pool.QueryRow(ctx, fmt.Sprintf(queries.CountByUserAndType, graphName), userName, memoryType).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.GetAllMemories, graphName), userName, memoryType, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	results, err := scanPaginatedMemoryRows(rows)
	return results, total, err
}

// GetMemoriesByFilter fetches activated memories for cube IDs matching SQL WHERE conditions.
func (p *Postgres) GetMemoriesByFilter(ctx context.Context, cubeIDs []string, filterConditions []string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 100
	}
	userConds := make([]string, 0, len(cubeIDs))
	for _, id := range cubeIDs {
		escaped := strings.ReplaceAll(id, "'", "''")
		userConds = append(userConds, fmt.Sprintf("properties->>(('user_name'::text)) = '%s'", escaped))
	}
	parts := make([]string, 0, 2+len(filterConditions))
	if len(userConds) == 1 {
		parts = append(parts, userConds[0])
	} else {
		parts = append(parts, "("+strings.Join(userConds, " OR ")+")")
	}
	parts = append(parts, "properties->>(('status'::text)) = 'activated'")
	parts = append(parts, filterConditions...)
	q := fmt.Sprintf(queries.GetMemoriesByFilterSQL, graphName, strings.Join(parts, " AND "), limit)
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("get memories by filter: %w", err)
	}
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var propsStr string
		if err := rows.Scan(&propsStr); err != nil {
			return nil, fmt.Errorf("get memories by filter scan: %w", err)
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(propsStr), &props); err != nil {
			continue
		}
		results = append(results, props)
	}
	return results, rows.Err()
}

// GetUserNamesByMemoryIDs maps property IDs to their user_name values.
func (p *Postgres) GetUserNamesByMemoryIDs(ctx context.Context, memoryIDs []string) (map[string]string, error) {
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.GetUserNamesByPropertyIDs, graphName), memoryIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var propID, userName string
		if err := rows.Scan(&propID, &userName); err != nil {
			return nil, err
		}
		result[propID] = userName
	}
	return result, rows.Err()
}
