package mcptools

import (
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// CreateCubeInput is the MCP input for create_cube.
type CreateCubeInput struct {
	CubeID      string         `json:"cube_id" jsonschema:"Unique identifier for the cube (required)"`
	OwnerID     string         `json:"owner_id,omitempty" jsonschema:"User ID of the cube owner"`
	UserID      string         `json:"user_id,omitempty" jsonschema:"Alias for owner_id"`
	CubeName    string         `json:"cube_name,omitempty" jsonschema:"Human-readable name (defaults to cube_id)"`
	Description string         `json:"description,omitempty" jsonschema:"Optional description"`
	CubePath    string         `json:"cube_path,omitempty" jsonschema:"Optional file system path"`
	Settings    map[string]any `json:"settings,omitempty" jsonschema:"Optional key-value settings"`
}

// ListCubesInput is the MCP input for list_cubes.
type ListCubesInput struct {
	OwnerID string `json:"owner_id,omitempty" jsonschema:"Filter by owner user ID"`
}

// DeleteCubeInput is the MCP input for delete_cube.
type DeleteCubeInput struct {
	CubeID     string `json:"cube_id" jsonschema:"Cube ID to delete (required)"`
	UserID     string `json:"user_id" jsonschema:"User ID of the requester — must be the cube owner (required)"`
	HardDelete bool   `json:"hard_delete,omitempty" jsonschema:"If true, also removes all memories in the cube"`
}

// GetUserCubesInput is the MCP input for get_user_cubes.
type GetUserCubesInput struct {
	UserID string `json:"user_id" jsonschema:"User ID whose cubes to list (required)"`
}

// cubeResultMap serializes a db.Cube to a JSON-compatible map for MCP responses.
func cubeResultMap(c db.Cube) map[string]any { return db.CubeToMap(c) }
