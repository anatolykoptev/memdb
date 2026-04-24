package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrCubeNotFound is returned by Get/SoftDelete/HardDelete when the cube row is missing.
var ErrCubeNotFound = errors.New("cube not found")

// Cube is a row from memos_graph.cubes.
type Cube struct {
	CubeID      string
	CubeName    string
	OwnerID     string
	Description *string
	CubePath    *string
	Settings    map[string]any
	CreatedAt   time.Time
	UpdatedAt   time.Time
	IsActive    bool
}

// UpsertCubeParams is the input to Postgres.UpsertCube.
type UpsertCubeParams struct {
	CubeID      string         // required
	CubeName    *string        // optional; defaults to CubeID on insert, preserved on update when nil
	OwnerID     string         // required on insert; IGNORED on update (owner transfer is a separate operation)
	Description *string        // optional; preserved on update when nil
	CubePath    *string        // optional; preserved on update when nil
	Settings    map[string]any // optional; preserved on update when nil
}

// UpsertCube inserts a new cube or updates metadata of an existing one.
// owner_id is preserved on update — transferring ownership requires a separate operation.
// Returns (cube, created, err). created is true iff a new row was inserted.
func (p *Postgres) UpsertCube(ctx context.Context, params UpsertCubeParams) (Cube, bool, error) {
	if params.CubeID == "" {
		return Cube{}, false, fmt.Errorf("UpsertCube: cube_id required")
	}
	if params.OwnerID == "" {
		return Cube{}, false, fmt.Errorf("UpsertCube: owner_id required on insert")
	}

	// Serialize settings to JSONB bytes. nil → NULL (triggers DEFAULT on insert, preserved on update).
	var settingsJSON []byte
	if params.Settings != nil {
		b, err := json.Marshal(params.Settings)
		if err != nil {
			return Cube{}, false, fmt.Errorf("marshal settings: %w", err)
		}
		settingsJSON = b
	}

	row := p.pool.QueryRow(ctx, `
INSERT INTO memos_graph.cubes (cube_id, cube_name, owner_id, description, cube_path, settings)
VALUES ($1, COALESCE($2, $1), $3, $4, $5, COALESCE($6::jsonb, '{}'::jsonb))
ON CONFLICT (cube_id) DO UPDATE SET
    cube_name   = COALESCE(EXCLUDED.cube_name, memos_graph.cubes.cube_name),
    description = COALESCE(EXCLUDED.description, memos_graph.cubes.description),
    cube_path   = COALESCE(EXCLUDED.cube_path, memos_graph.cubes.cube_path),
    settings    = COALESCE(EXCLUDED.settings, memos_graph.cubes.settings),
    updated_at  = NOW()
RETURNING cube_id, cube_name, owner_id, description, cube_path, settings, created_at, updated_at, is_active, (xmax = 0) AS inserted
`,
		params.CubeID,
		params.CubeName,
		params.OwnerID,
		params.Description,
		params.CubePath,
		settingsJSON,
	)

	var c Cube
	var settingsRaw []byte
	var inserted bool
	if err := row.Scan(
		&c.CubeID, &c.CubeName, &c.OwnerID, &c.Description, &c.CubePath,
		&settingsRaw, &c.CreatedAt, &c.UpdatedAt, &c.IsActive, &inserted,
	); err != nil {
		return Cube{}, false, fmt.Errorf("UpsertCube scan: %w", err)
	}
	if len(settingsRaw) > 0 {
		_ = json.Unmarshal(settingsRaw, &c.Settings)
	}
	return c, inserted, nil
}

// SoftDeleteCube marks a cube inactive. Memories remain, but list_cubes hides it.
// Returns ErrCubeNotFound when no row matches.
func (p *Postgres) SoftDeleteCube(ctx context.Context, cubeID string) error {
	tag, err := p.pool.Exec(ctx, `
UPDATE memos_graph.cubes
SET is_active = FALSE, updated_at = NOW()
WHERE cube_id = $1
`, cubeID)
	if err != nil {
		return fmt.Errorf("SoftDeleteCube: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCubeNotFound
	}
	return nil
}

// HardDeleteCube soft-deletes the cube AND removes all Memory rows tagged to it.
// Runs in a single transaction; returns the number of memories removed.
func (p *Postgres) HardDeleteCube(ctx context.Context, cubeID string) (int64, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("HardDeleteCube begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
UPDATE memos_graph.cubes SET is_active = FALSE, updated_at = NOW() WHERE cube_id = $1
`, cubeID); err != nil {
		return 0, fmt.Errorf("HardDeleteCube soft-delete cube row: %w", err)
	}

	tag, err := tx.Exec(ctx, `
DELETE FROM memos_graph."Memory" WHERE properties->>(('user_name'::text)) = $1
`, cubeID)
	if err != nil {
		return 0, fmt.Errorf("HardDeleteCube delete memories: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("HardDeleteCube commit: %w", err)
	}
	return tag.RowsAffected(), nil
}

// EnsureCubeExists upserts a minimal cube row. Used by NativeAdd for hybrid auto-create.
// Returns (created, err). created is true iff a new row was inserted.
func (p *Postgres) EnsureCubeExists(ctx context.Context, cubeID, ownerID string) (bool, error) {
	if cubeID == "" || ownerID == "" {
		return false, fmt.Errorf("EnsureCubeExists: cube_id and owner_id required")
	}
	var inserted bool
	err := p.pool.QueryRow(ctx, `
INSERT INTO memos_graph.cubes (cube_id, cube_name, owner_id)
VALUES ($1, $1, $2)
ON CONFLICT (cube_id) DO UPDATE SET updated_at = NOW()
RETURNING (xmax = 0) AS inserted
`, cubeID, ownerID).Scan(&inserted)
	if err != nil {
		return false, fmt.Errorf("EnsureCubeExists: %w", err)
	}
	return inserted, nil
}
