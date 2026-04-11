package db

// postgres_cubes_read.go — read-only queries for the cubes table: ListCubes, GetCube.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ListCubes returns all active cubes. If ownerID is non-nil, filters by owner.
// Ordered by updated_at DESC.
func (p *Postgres) ListCubes(ctx context.Context, ownerID *string) ([]Cube, error) {
	rows, err := p.pool.Query(ctx, `
SELECT cube_id, cube_name, owner_id, description, cube_path, settings, created_at, updated_at, is_active
FROM memos_graph.cubes
WHERE is_active = TRUE
  AND ($1::text IS NULL OR owner_id = $1)
ORDER BY updated_at DESC
`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("ListCubes query: %w", err)
	}
	defer rows.Close()

	var out []Cube
	for rows.Next() {
		var c Cube
		var settingsRaw []byte
		if err := rows.Scan(
			&c.CubeID, &c.CubeName, &c.OwnerID, &c.Description, &c.CubePath,
			&settingsRaw, &c.CreatedAt, &c.UpdatedAt, &c.IsActive,
		); err != nil {
			return nil, fmt.Errorf("ListCubes scan: %w", err)
		}
		if len(settingsRaw) > 0 {
			_ = json.Unmarshal(settingsRaw, &c.Settings)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListCubes iterate: %w", err)
	}
	return out, nil
}

// GetCube fetches a single cube by ID (including inactive ones — caller filters).
// Returns ErrCubeNotFound when the cube row does not exist.
func (p *Postgres) GetCube(ctx context.Context, cubeID string) (*Cube, error) {
	row := p.pool.QueryRow(ctx, `
SELECT cube_id, cube_name, owner_id, description, cube_path, settings, created_at, updated_at, is_active
FROM memos_graph.cubes
WHERE cube_id = $1
`, cubeID)
	var c Cube
	var settingsRaw []byte
	if err := row.Scan(
		&c.CubeID, &c.CubeName, &c.OwnerID, &c.Description, &c.CubePath,
		&settingsRaw, &c.CreatedAt, &c.UpdatedAt, &c.IsActive,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCubeNotFound
		}
		return nil, err
	}
	if len(settingsRaw) > 0 {
		_ = json.Unmarshal(settingsRaw, &c.Settings)
	}
	return &c, nil
}
