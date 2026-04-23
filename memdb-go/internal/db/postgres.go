// Package db provides database clients for Phase 2 native handlers.
//
// File layout:
//   postgres.go        — core: Postgres struct, NewPostgres, Pool/Ping/Close
//   postgres_memory.go — memory node CRUD: Get/List/Insert/Update/Delete/Cleanup
//   postgres_search.go — vector & fulltext search, FormatVector, importance decay
//   postgres_graph.go  — graph recall (key/tags/BFS/edge), memory_edges table
//   postgres_entity.go — entity_nodes table, entity upsert & lookup
//   postgres_config.go — user_configs table, GetUserConfig/UpdateUserConfig
package db

import (
"context"
"errors"
"fmt"
"log/slog"
"time"

"github.com/anatolykoptev/go-kit/retry"
"github.com/jackc/pgx/v5"
"github.com/jackc/pgx/v5/pgxpool"

"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
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
return nil, errors.New("postgres connection string is empty")
}

cfg, err := pgxpool.ParseConfig(connStr)
if err != nil {
return nil, fmt.Errorf("invalid postgres config: %w", err)
}
cfg.MaxConns = 8
cfg.MinConns = 2
cfg.MaxConnLifetime = 30 * time.Minute
cfg.MaxConnLifetimeJitter = 5 * time.Minute // spread connection recycling to avoid thundering herd
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
// pgvector 0.8.x: iterative HNSW scan keeps scanning past the WHERE filter
// until ef_search candidates are found — critical for filtered queries.
_, err = conn.Exec(ctx, "SET hnsw.iterative_scan = relaxed_order; SET hnsw.ef_search = 100")
if err != nil {
logger.Warn("failed to set hnsw session params", slog.Any("error", err))
}
return nil // non-fatal — queries use fully-qualified table names
}

pool, err := retry.Do(ctx, retry.Options{
MaxAttempts:  10,
InitialDelay: time.Second,
MaxDelay:     30 * time.Second,
Jitter:       true,
OnRetry: func(attempt int, err error) {
	logger.Warn("postgres not ready, retrying", slog.Int("attempt", attempt), slog.Any("error", err))
},
}, func() (*pgxpool.Pool, error) {
p, retryErr := pgxpool.NewWithConfig(ctx, cfg)
if retryErr != nil {
	return nil, retryErr
}
if retryErr = p.Ping(ctx); retryErr != nil {
	p.Close()
	return nil, retryErr
}
return p, nil
})
if err != nil {
return nil, fmt.Errorf("postgres connect: %w", err)
}

pg := &Postgres{pool: pool, logger: logger}

// Best-effort: create memory_edges table on startup (idempotent CREATE IF NOT EXISTS).
if err := pg.EnsureEdgesTable(ctx); err != nil {
logger.Warn("memory_edges table init failed (graph edges disabled)", slog.Any("error", err))
}

// Best-effort: create entity_nodes table (entity identity resolution + embedding HNSW index).
if err := pg.EnsureEntityNodesTable(ctx); err != nil {
logger.Warn("entity_nodes table init failed (entity graph disabled)", slog.Any("error", err))
}

// Best-effort: create entity_edges table on startup (idempotent CREATE IF NOT EXISTS).
if err := pg.EnsureEntityEdgesTable(ctx); err != nil {
logger.Warn("entity_edges table init failed (entity triplets disabled)", slog.Any("error", err))
}

// Best-effort: create user_configs table.
if err := pg.EnsureUserConfigsTable(ctx); err != nil {
logger.Warn("user_configs table init failed", slog.Any("error", err))
}

// Versioned SQL migrations. Fail-fast: schema drift must crash startup
// so dozor flags it — not silently log-and-continue like Ensure* tables.
if err := pg.RunMigrations(ctx); err != nil {
pool.Close()
return nil, fmt.Errorf("run migrations: %w", err)
}

logger.Info("postgres connected", slog.Int("max_conns", int(cfg.MaxConns)))
return pg, nil
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
