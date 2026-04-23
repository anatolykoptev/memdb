package db

// postgres_migrations.go — versioned SQL migration runner for memdb-go.
// Replaces Python graph_dbs/polardb/schema.py (de-facto schema bootstrap).
//
// Semantics:
//   - Advisory lock on a fixed key prevents concurrent apply across replicas.
//   - schema_migrations tracks (name, sha256 checksum, applied_at).
//   - Baseline: if memos_graph.cubes exists but schema_migrations is empty,
//     mark 0001 as applied without executing it — avoids re-running invariant
//     RAISE block against already-mutated data.
//   - Each pending migration runs in its own transaction; content + insert
//     commit atomically. Failure rolls back — next startup retries cleanly.
//   - Checksum drift: if an applied file's sha256 differs from current bytes,
//     emit a Warn but do NOT re-apply. Manual intervention required.
//   - Fail-fast: any error returns. Caller (NewPostgres) must propagate.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockKey — fixed int8 namespace for pg_advisory_lock.
// ASCII "MEMDB_M\0" → 0x4D454D44425F4D00. Unlikely to collide with
// app-level advisory locks (which use smaller ints).
const migrationLockKey int64 = 0x4D454D44425F4D00

// firstMigrationName is the migration that was applied manually on prod
// before the runner existed. Baseline marks it applied without executing.
const firstMigrationName = "0001_phase2_user_cube_split.sql"

// RunMigrations applies all pending embedded SQL migrations in lex order.
// Idempotent. Must be called exactly once per NewPostgres.
// Returns error — caller must propagate (do not log-and-swallow).
func (p *Postgres) RunMigrations(ctx context.Context) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockKey); err != nil {
			p.logger.Warn("release migration advisory lock failed", slog.Any("error", err))
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS memos_graph.schema_migrations (
			name       TEXT        PRIMARY KEY,
			checksum   TEXT        NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	if err := p.baselineIfNeeded(ctx, conn); err != nil {
		return fmt.Errorf("baseline existing schema: %w", err)
	}

	applied, err := p.loadAppliedMigrations(ctx, conn)
	if err != nil {
		return fmt.Errorf("load applied migrations: %w", err)
	}

	files, err := listMigrationFiles()
	if err != nil {
		return fmt.Errorf("list migration files: %w", err)
	}

	for _, name := range files {
		content, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256sum(content)

		if prev, ok := applied[name]; ok {
			if prev != sum {
				p.logger.Warn("migration checksum drift",
					slog.String("name", name),
					slog.String("applied_sha", prev[:12]),
					slog.String("current_sha", sum[:12]),
					slog.String("action", "no re-apply; manual intervention required"))
			}
			continue
		}

		p.logger.Info("applying migration", slog.String("name", name), slog.String("sha", sum[:12]))
		if err := p.applyMigration(ctx, conn, name, string(content), sum); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		p.logger.Info("migration applied", slog.String("name", name))
	}
	return nil
}

// baselineIfNeeded handles transition from Python-managed schema to Go runner.
// If memos_graph.cubes already exists and schema_migrations is empty, marks
// firstMigrationName applied without executing it. Safe no-op on fresh DB.
func (p *Postgres) baselineIfNeeded(ctx context.Context, conn *pgxpool.Conn) error {
	var count int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM memos_graph.schema_migrations`,
	).Scan(&count); err != nil {
		return fmt.Errorf("count schema_migrations: %w", err)
	}
	if count > 0 {
		return nil
	}

	var cubesExists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_tables
			WHERE schemaname = 'memos_graph' AND tablename = 'cubes'
		)`).Scan(&cubesExists); err != nil {
		return fmt.Errorf("probe cubes existence: %w", err)
	}
	if !cubesExists {
		return nil
	}

	content, err := migrations.FS.ReadFile(firstMigrationName)
	if err != nil {
		return fmt.Errorf("read %s for baseline: %w", firstMigrationName, err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO memos_graph.schema_migrations(name, checksum)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING`,
		firstMigrationName, sha256sum(content)); err != nil {
		return fmt.Errorf("baseline insert: %w", err)
	}
	p.logger.Info("baselined existing schema",
		slog.String("name", firstMigrationName),
		slog.String("reason", "memos_graph.cubes exists, schema_migrations was empty"))
	return nil
}

func (p *Postgres) loadAppliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[string]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT name, checksum FROM memos_graph.schema_migrations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := make(map[string]string)
	for rows.Next() {
		var name, sum string
		if err := rows.Scan(&name, &sum); err != nil {
			return nil, err
		}
		applied[name] = sum
	}
	return applied, rows.Err()
}

func (p *Postgres) applyMigration(ctx context.Context, conn *pgxpool.Conn, name, content, sum string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, content); err != nil {
		return fmt.Errorf("exec body: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO memos_graph.schema_migrations(name, checksum) VALUES ($1, $2)`,
		name, sum,
	); err != nil {
		return fmt.Errorf("insert schema_migrations row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func listMigrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func sha256sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
