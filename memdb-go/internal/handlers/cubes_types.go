package handlers

import (
	"context"

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

// workingMemoryCacher is the narrow interface used by handlers that need to
// manage the Redis VSET hot-cache for WorkingMemory nodes.
// Implemented by *db.WorkingMemoryCache in production and by fakeWMCache in tests.
type workingMemoryCacher interface {
	VAdd(ctx context.Context, cubeID, nodeID, memoryText string, embedding []float32, ts int64) error
	VRem(ctx context.Context, cubeID, nodeID string) error
	VRemBatch(ctx context.Context, cubeID string, nodeIDs []string) error
	VSim(ctx context.Context, cubeID string, queryEmbedding []float32, topN int) ([]db.VSetCandidate, error)
	VDrop(ctx context.Context, cubeID string) error
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
func cubeToMap(c db.Cube) map[string]any { return db.CubeToMap(c) }
