// Package db provides database clients for Phase 2 native handlers.
package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres connect failed: %w", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}

	// Ensure Apache AGE extension is loaded for this connection pool
	_, err = pool.Exec(ctx, "LOAD 'age'")
	if err != nil {
		logger.Warn("AGE extension load failed (may not be installed)", slog.Any("error", err))
	}

	_, err = pool.Exec(ctx, "SET search_path = ag_catalog, memos_graph, public")
	if err != nil {
		logger.Warn("failed to set search_path", slog.Any("error", err))
	}

	logger.Info("postgres connected", slog.Int("max_conns", int(cfg.MaxConns)))
	return &Postgres{pool: pool, logger: logger}, nil
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
