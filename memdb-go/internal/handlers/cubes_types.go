package handlers

import (
	"context"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// cubeStoreClient is the narrow interface used by cube handlers.
// Implemented by *db.Postgres in production and by fakeCubeStore in tests.
type cubeStoreClient interface {
	UpsertCube(ctx context.Context, params db.UpsertCubeParams) (db.Cube, bool, error)
	ListCubes(ctx context.Context, ownerID *string) ([]db.Cube, error)
	GetCube(ctx context.Context, cubeID string) (*db.Cube, error)
	SoftDeleteCube(ctx context.Context, cubeID string) error
	HardDeleteCube(ctx context.Context, cubeID string) (int64, error)
	EnsureCubeExists(ctx context.Context, cubeID, ownerID string) (bool, error)
}

// createCubeRequest is the body of POST /product/create_cube.
type createCubeRequest struct {
	CubeID      string         `json:"cube_id"`
	CubeName    *string        `json:"cube_name,omitempty"`
	OwnerID     *string        `json:"owner_id,omitempty"` // optional; defaults to request user_id
	UserID      *string        `json:"user_id,omitempty"`  // fallback owner source
	Description *string        `json:"description,omitempty"`
	CubePath    *string        `json:"cube_path,omitempty"`
	Settings    map[string]any `json:"settings,omitempty"`
}

// listCubesRequest is the body of POST /product/list_cubes.
type listCubesRequest struct {
	OwnerID *string `json:"owner_id,omitempty"`
}

// deleteCubeRequest is the body of POST /product/delete_cube.
type deleteCubeRequest struct {
	CubeID     string `json:"cube_id"`
	UserID     string `json:"user_id"`
	HardDelete bool   `json:"hard_delete,omitempty"`
}

// getUserCubesRequest is the body of POST /product/get_user_cubes.
type getUserCubesRequest struct {
	UserID string `json:"user_id"`
}

// cubeToMap serializes a db.Cube to a JSON-compatible map.
func cubeToMap(c db.Cube) map[string]any {
	m := map[string]any{
		"cube_id":    c.CubeID,
		"cube_name":  c.CubeName,
		"owner_id":   c.OwnerID,
		"is_active":  c.IsActive,
		"created_at": c.CreatedAt.Format(time.RFC3339),
		"updated_at": c.UpdatedAt.Format(time.RFC3339),
		"settings":   c.Settings,
	}
	if c.Description != nil {
		m["description"] = *c.Description
	}
	if c.CubePath != nil {
		m["cube_path"] = *c.CubePath
	}
	return m
}
