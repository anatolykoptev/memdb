// Package db provides database clients for Phase 2 native handlers.
package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MemDBai/MemDB/memdb-go/internal/db/queries"
)

// graphName is the fixed PolarDB graph name. All queries use this constant.
const graphName = queries.DefaultGraphName

// Postgres wraps a pgx connection pool for PolarDB (PostgreSQL + Apache AGE).
type Postgres struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPostgres creates a new PostgreSQL connection pool.
// The connStr should be a standard PostgreSQL connection string.
func NewPostgres(ctx context.Context, connStr string, logger *slog.Logger) (*Postgres, error) {
	if connStr == "" {
		return nil, fmt.Errorf("postgres connection string is empty")
	}

	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("invalid postgres config: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	// Run LOAD 'age' and SET search_path on every new connection in the pool.
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "LOAD 'age'")
		if err != nil {
			logger.Warn("AGE extension load failed on connection", slog.Any("error", err))
		}
		_, err = conn.Exec(ctx, "SET search_path = ag_catalog, memos_graph, public")
		if err != nil {
			logger.Warn("failed to set search_path on connection", slog.Any("error", err))
		}
		return nil // non-fatal — queries use fully-qualified table names
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres connect failed: %w", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}

	logger.Info("postgres connected", slog.Int("max_conns", int(cfg.MaxConns)))
	return &Postgres{pool: pool, logger: logger}, nil
}

// NewStubPostgres creates a Postgres stub with no connection pool.
// For testing only: the nil-check (postgres != nil) will pass, but any
// actual DB query will panic. Use this to test validation paths.
func NewStubPostgres() *Postgres {
	return &Postgres{}
}

// Pool returns the underlying pgx pool for direct query access.
func (p *Postgres) Pool() *pgxpool.Pool {
	return p.pool
}

// Ping checks the database connection.
func (p *Postgres) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

// Close closes the connection pool.
func (p *Postgres) Close() {
	p.pool.Close()
}

// GetMemoryByID retrieves a single memory node by its AGE graph ID.
// Returns the node properties as a JSON-compatible map, or nil if not found.
func (p *Postgres) GetMemoryByID(ctx context.Context, memoryID string) (map[string]any, error) {
	query := `
		SELECT id(v)::text AS memory_id,
		       v.properties::jsonb AS properties
		FROM "memos_graph"."Memory" v
		WHERE id(v)::text = $1
		LIMIT 1
	`

	var id string
	var propsJSON []byte
	err := p.pool.QueryRow(ctx, query, memoryID).Scan(&id, &propsJSON)
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"memory_id":  id,
		"properties": string(propsJSON),
	}
	return result, nil
}

// GetMemoryByIDs retrieves multiple memory nodes by their AGE graph IDs.
func (p *Postgres) GetMemoryByIDs(ctx context.Context, memoryIDs []string) ([]map[string]any, error) {
	query := `
		SELECT id(v)::text AS memory_id,
		       v.properties::jsonb AS properties
		FROM "memos_graph"."Memory" v
		WHERE id(v)::text = ANY($1)
	`

	rows, err := p.pool.Query(ctx, query, memoryIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id string
		var propsJSON []byte
		if err := rows.Scan(&id, &propsJSON); err != nil {
			return nil, err
		}
		results = append(results, map[string]any{
			"memory_id":  id,
			"properties": string(propsJSON),
		})
	}
	return results, rows.Err()
}

// ListUsers returns distinct user_name values from activated memories.
func (p *Postgres) ListUsers(ctx context.Context) ([]string, error) {
	q := fmt.Sprintf(queries.ListUsers, graphName)
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		users = append(users, name)
	}
	return users, rows.Err()
}

// CountDistinctUsers returns the number of distinct users with activated memories.
func (p *Postgres) CountDistinctUsers(ctx context.Context) (int, error) {
	q := fmt.Sprintf(queries.CountDistinctUsers, graphName)
	var count int
	err := p.pool.QueryRow(ctx, q).Scan(&count)
	return count, err
}

// ExistUser checks whether a user has any activated memories.
func (p *Postgres) ExistUser(ctx context.Context, userName string) (bool, error) {
	q := fmt.Sprintf(queries.ExistUser, graphName)
	var exists bool
	err := p.pool.QueryRow(ctx, q, userName).Scan(&exists)
	return exists, err
}

// GetAllMemories returns paginated memories for a user filtered by memory_type.
// Returns (results, totalCount, error).
func (p *Postgres) GetAllMemories(ctx context.Context, userName, memoryType string, page, pageSize int) ([]map[string]any, int, error) {
	offset := page * pageSize

	// Get total count
	countQ := fmt.Sprintf(queries.CountByUserAndType, graphName)
	var total int
	if err := p.pool.QueryRow(ctx, countQ, userName, memoryType).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Get paginated results
	q := fmt.Sprintf(queries.GetAllMemories, graphName)
	rows, err := p.pool.Query(ctx, q, userName, memoryType, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id string
		var propsJSON []byte
		if err := rows.Scan(&id, &propsJSON); err != nil {
			return nil, 0, err
		}
		results = append(results, map[string]any{
			"memory_id":  id,
			"properties": string(propsJSON),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return results, total, nil
}

// DeleteByPropertyIDs deletes nodes matching the given property IDs and user name.
// Returns the number of rows deleted.
func (p *Postgres) DeleteByPropertyIDs(ctx context.Context, propertyIDs []string, userName string) (int64, error) {
	q := fmt.Sprintf(queries.DeleteByPropertyIDs, graphName)
	tag, err := p.pool.Exec(ctx, q, propertyIDs, userName)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GetUserNamesByMemoryIDs maps property IDs to their user_name values.
func (p *Postgres) GetUserNamesByMemoryIDs(ctx context.Context, memoryIDs []string) (map[string]string, error) {
	q := fmt.Sprintf(queries.GetUserNamesByPropertyIDs, graphName)
	rows, err := p.pool.Query(ctx, q, memoryIDs)
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

// UpdateMemoryContent updates the memory text for a given memory node.
// Returns true if a row was updated, false if the memory was not found.
func (p *Postgres) UpdateMemoryContent(ctx context.Context, memoryID, content string) (bool, error) {
	q := fmt.Sprintf(queries.UpdateMemoryContent, graphName)
	tag, err := p.pool.Exec(ctx, q, memoryID, content)
	if err != nil {
		return false, fmt.Errorf("update memory: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteAllByUser deletes all activated memories for a user.
// Returns the number of rows deleted.
func (p *Postgres) DeleteAllByUser(ctx context.Context, userName string) (int64, error) {
	q := fmt.Sprintf(queries.DeleteAllByUser, graphName)
	tag, err := p.pool.Exec(ctx, q, userName)
	if err != nil {
		return 0, fmt.Errorf("delete all by user: %w", err)
	}
	return tag.RowsAffected(), nil
}

// VectorSearchResult holds a single result from vector or fulltext search.
type VectorSearchResult struct {
	ID         string  // AGE node ID (text)
	Properties string  // raw JSON properties
	Score      float64 // similarity/rank score
}

// FormatVector formats a float32 slice as a pgvector string literal: '[0.1,0.2,...]'.
func FormatVector(vec []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", v)
	}
	b.WriteByte(']')
	return b.String()
}

// VectorSearch performs cosine similarity search across multiple memory types.
// Returns results sorted by similarity score (descending).
func (p *Postgres) VectorSearch(ctx context.Context, vector []float32, userName string, memoryTypes []string, limit int) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearch, graphName)
	rows, err := p.pool.Query(ctx, q, vecStr, userName, memoryTypes, limit)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.Score); err != nil {
			return nil, fmt.Errorf("vector search scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// FulltextSearch performs tsvector fulltext search across multiple memory types.
// The tsquery should be pre-built (e.g. "token1 | token2 | token3").
func (p *Postgres) FulltextSearch(ctx context.Context, tsquery string, userName string, memoryTypes []string, limit int) ([]VectorSearchResult, error) {
	if tsquery == "" {
		return nil, nil
	}
	q := fmt.Sprintf(queries.FulltextSearch, graphName)
	rows, err := p.pool.Query(ctx, q, tsquery, userName, memoryTypes, limit)
	if err != nil {
		return nil, fmt.Errorf("fulltext search: %w", err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.Score); err != nil {
			return nil, fmt.Errorf("fulltext search scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
