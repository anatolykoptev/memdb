package db

// postgres_cubes_test_helpers.go — DDL and cleanup helpers for cube integration tests.
// DO NOT call these from production code.

import "context"

// EnsureCubesTable creates the cubes table and its indexes if missing.
// Used by integration tests; the production migration (migrations/0001_*.sql) is the canonical DDL source.
func (p *Postgres) EnsureCubesTable(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS memos_graph.cubes (
    cube_id     TEXT        PRIMARY KEY,
    cube_name   TEXT        NOT NULL,
    owner_id    TEXT        NOT NULL,
    description TEXT,
    cube_path   TEXT,
    settings    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_active   BOOLEAN     NOT NULL DEFAULT TRUE
);
CREATE INDEX IF NOT EXISTS idx_cubes_owner      ON memos_graph.cubes (owner_id)        WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_cubes_path       ON memos_graph.cubes (cube_path)       WHERE cube_path IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_cubes_updated_at ON memos_graph.cubes (updated_at DESC) WHERE is_active;
`)
	return err
}

// DeleteCubesByPrefix removes cubes whose cube_id starts with the given prefix.
// Intended for test cleanup only — do NOT call from production code.
func (p *Postgres) DeleteCubesByPrefix(ctx context.Context, prefix string) error {
	_, err := p.pool.Exec(ctx, `
DELETE FROM memos_graph.cubes WHERE cube_id LIKE $1
`, prefix+"%")
	return err
}
