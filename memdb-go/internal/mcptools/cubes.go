package mcptools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterCubeTools registers create_cube, list_cubes, delete_cube, and get_user_cubes.
// All tools call db.Postgres directly — no HTTP round-trip.
func RegisterCubeTools(server *mcp.Server, pg *db.Postgres, logger *slog.Logger) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_cube",
		Description: "Create a new memory cube for a user. Idempotent: calling with the same cube_id updates metadata without changing the owner.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input CreateCubeInput) (*mcp.CallToolResult, TextResult, error) {
		return handleCreateCube(ctx, pg, input)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_cubes",
		Description: "List all memory cubes, optionally filtered by owner_id.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ListCubesInput) (*mcp.CallToolResult, TextResult, error) {
		return handleListCubes(ctx, pg, input)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_cube",
		Description: "Delete a memory cube. Only the owner can delete. Soft-delete by default; set hard_delete=true to also remove all stored memories.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DeleteCubeInput) (*mcp.CallToolResult, TextResult, error) {
		return handleDeleteCube(ctx, pg, input, logger)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_user_cubes",
		Description: "List all memory cubes owned by a specific user.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input GetUserCubesInput) (*mcp.CallToolResult, TextResult, error) {
		return handleGetUserCubes(ctx, pg, input)
	})
}

func handleCreateCube(ctx context.Context, pg *db.Postgres, input CreateCubeInput) (*mcp.CallToolResult, TextResult, error) {
	if input.CubeID == "" {
		return nil, TextResult{}, errors.New("cube_id is required")
	}
	ownerID := input.OwnerID
	if ownerID == "" {
		ownerID = input.UserID
	}
	if ownerID == "" {
		return nil, TextResult{}, errors.New("owner_id or user_id is required")
	}

	params := db.UpsertCubeParams{CubeID: input.CubeID, OwnerID: ownerID}
	if input.CubeName != "" {
		params.CubeName = &input.CubeName
	}
	if input.Description != "" {
		params.Description = &input.Description
	}
	if input.CubePath != "" {
		params.CubePath = &input.CubePath
	}
	if len(input.Settings) > 0 {
		params.Settings = input.Settings
	}

	cube, created, err := pg.UpsertCube(ctx, params)
	if err != nil {
		return nil, TextResult{}, fmt.Errorf("create_cube failed: %w", err)
	}
	return nil, TextResult{Result: map[string]any{
		"cube":    cubeResultMap(cube),
		"created": created,
	}}, nil
}

func handleListCubes(ctx context.Context, pg *db.Postgres, input ListCubesInput) (*mcp.CallToolResult, TextResult, error) {
	var ownerFilter *string
	if input.OwnerID != "" {
		ownerFilter = &input.OwnerID
	}
	cubes, err := pg.ListCubes(ctx, ownerFilter)
	if err != nil {
		return nil, TextResult{}, fmt.Errorf("list_cubes failed: %w", err)
	}
	out := make([]map[string]any, 0, len(cubes))
	for _, c := range cubes {
		out = append(out, cubeResultMap(c))
	}
	return nil, TextResult{Result: map[string]any{"cubes": out}}, nil
}

func handleDeleteCube(ctx context.Context, pg *db.Postgres, input DeleteCubeInput, logger *slog.Logger) (*mcp.CallToolResult, TextResult, error) {
	if input.CubeID == "" {
		return nil, TextResult{}, errors.New("cube_id is required")
	}
	if input.UserID == "" {
		return nil, TextResult{}, errors.New("user_id is required")
	}

	cube, err := pg.GetCube(ctx, input.CubeID)
	if err != nil {
		if errors.Is(err, db.ErrCubeNotFound) {
			return nil, TextResult{Result: map[string]any{"deleted": false, "reason": "cube not found"}}, nil
		}
		return nil, TextResult{}, fmt.Errorf("delete_cube lookup failed: %w", err)
	}
	if cube.OwnerID != input.UserID {
		return nil, TextResult{}, fmt.Errorf("delete_cube forbidden: user %q is not the owner of cube %q", input.UserID, input.CubeID)
	}

	if input.HardDelete {
		memoriesDeleted, err := pg.HardDeleteCube(ctx, input.CubeID)
		if err != nil {
			logger.Error("delete_cube hard failed", slog.String("cube_id", input.CubeID), slog.Any("error", err))
			return nil, TextResult{}, fmt.Errorf("delete_cube hard failed: %w", err)
		}
		return nil, TextResult{Result: map[string]any{
			"cube_id":          input.CubeID,
			"deleted":          true,
			"hard_delete":      true,
			"memories_removed": memoriesDeleted,
		}}, nil
	}

	if err := pg.SoftDeleteCube(ctx, input.CubeID); err != nil {
		logger.Error("delete_cube soft failed", slog.String("cube_id", input.CubeID), slog.Any("error", err))
		return nil, TextResult{}, fmt.Errorf("delete_cube soft failed: %w", err)
	}
	return nil, TextResult{Result: map[string]any{
		"cube_id":     input.CubeID,
		"deleted":     true,
		"hard_delete": false,
	}}, nil
}

func handleGetUserCubes(ctx context.Context, pg *db.Postgres, input GetUserCubesInput) (*mcp.CallToolResult, TextResult, error) {
	if input.UserID == "" {
		return nil, TextResult{}, errors.New("user_id is required")
	}
	cubes, err := pg.ListCubes(ctx, &input.UserID)
	if err != nil {
		return nil, TextResult{}, fmt.Errorf("get_user_cubes failed: %w", err)
	}
	out := make([]map[string]any, 0, len(cubes))
	for _, c := range cubes {
		out = append(out, cubeResultMap(c))
	}
	return nil, TextResult{Result: map[string]any{"user_id": input.UserID, "cubes": out}}, nil
}
