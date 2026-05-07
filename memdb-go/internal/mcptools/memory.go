package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterMemoryTools registers get_memory, delete_memory, and delete_all_memories.
//
// NOTE: update_memory is intentionally NOT registered here. It requires full
// re-embedding (ONNX) and CE cache invalidation — those only run in the
// memdb-go server process. update_memory is registered in
// RegisterNativeGoProxyTools and proxied to /product/update_memory so both
// REST and MCP follow the same code path.
func RegisterMemoryTools(server *mcp.Server, pg *db.Postgres, qd *db.Qdrant, logger *slog.Logger) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_memory",
		Description: "Retrieve a specific memory by its unique identifier from a memory cube.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input GetMemoryInput) (*mcp.CallToolResult, TextResult, error) {
		return handleGetMemory(ctx, pg, input)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_memory",
		Description: "Permanently delete a specific memory from a cube.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DeleteMemoryInput) (*mcp.CallToolResult, TextResult, error) {
		return handleDeleteMemory(ctx, pg, qd, input, logger)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_all_memories",
		Description: "Permanently delete all memories from a specific cube. Use with caution.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DeleteAllMemoriesInput) (*mcp.CallToolResult, TextResult, error) {
		return handleDeleteAllMemories(ctx, pg, qd, input, logger)
	})
}

func handleGetMemory(ctx context.Context, pg *db.Postgres, input GetMemoryInput) (*mcp.CallToolResult, TextResult, error) {
	if input.MemoryID == "" {
		return nil, TextResult{}, errors.New("memory_id is required")
	}
	result, err := pg.GetMemoryByPropertyID(ctx, input.MemoryID)
	if err != nil {
		return nil, TextResult{}, fmt.Errorf("get_memory failed: %w", err)
	}
	if result == nil {
		return nil, TextResult{Result: "memory not found"}, nil
	}
	if propsStr, ok := result["properties"].(string); ok {
		var props map[string]any
		if json.Unmarshal([]byte(propsStr), &props) == nil {
			return nil, TextResult{Result: search.FormatMemoryItem(props, false)}, nil
		}
	}
	return nil, TextResult{Result: result}, nil
}

// handleUpdateMemory proxies the update to memdb-go's /product/update_memory.
// This ensures the full update path runs (re-embed + CE cache invalidation),
// matching what the REST endpoint does. Using the text-only UpdateMemoryContent
// DB call would silently stale vector search and cross-encoder rerank results.
//
// The function signature includes memdbGoURL and serviceSecret (not *db.Postgres)
// because Postgres is not needed — all DB side-effects happen inside memdb-go.
func handleUpdateMemory(ctx context.Context, _ *db.Postgres, memdbGoURL, serviceSecret string, input UpdateMemoryInput) (*mcp.CallToolResult, TextResult, error) {
	if input.MemoryID == "" {
		return nil, TextResult{}, errors.New("memory_id is required")
	}
	if input.MemoryContent == "" {
		return nil, TextResult{}, errors.New("memory_content is required")
	}

	// Resolve the user/cube identifier: prefer explicit UserID, fall back to CubeID.
	cubeID := input.UserID
	if cubeID == "" {
		cubeID = input.CubeID
	}

	// Build the REST payload expected by NativeUpdateMemory.
	payload := map[string]any{
		"memory_id": input.MemoryID,
		"user_id":   cubeID,
		"text":      input.MemoryContent,
	}

	result, err := proxyCall(ctx, memdbGoURL, "/product/update_memory", serviceSecret, "update_memory", payload, nil)
	if err != nil {
		return nil, TextResult{}, fmt.Errorf("update_memory failed: %w", err)
	}
	return nil, result, nil
}

// prefCollections are the Qdrant collections for preference memory.
var prefCollections = []string{"explicit_preference", "implicit_preference"}

func handleDeleteMemory(ctx context.Context, pg *db.Postgres, qd *db.Qdrant, input DeleteMemoryInput, logger *slog.Logger) (*mcp.CallToolResult, TextResult, error) {
	if input.MemoryID == "" {
		return nil, TextResult{}, errors.New("memory_id is required")
	}
	userName := input.UserID
	if userName == "" {
		userName = defaultUserID
	}
	deleted, err := pg.DeleteByPropertyIDs(ctx, []string{input.MemoryID}, userName)
	if err != nil {
		return nil, TextResult{}, fmt.Errorf("delete_memory failed: %w", err)
	}
	// Also clean Qdrant preference collections to prevent ghost vectors.
	if qd != nil {
		for _, coll := range prefCollections {
			if err := qd.DeleteByIDs(ctx, coll, []string{input.MemoryID}); err != nil {
				logger.Warn("mcp delete_memory: qdrant cleanup failed", slog.String("collection", coll), slog.Any("error", err))
			}
		}
	}
	return nil, TextResult{Result: map[string]any{"memory_id": input.MemoryID, "deleted_count": deleted}}, nil
}

func handleDeleteAllMemories(ctx context.Context, pg *db.Postgres, qd *db.Qdrant, input DeleteAllMemoriesInput, logger *slog.Logger) (*mcp.CallToolResult, TextResult, error) {
	if input.CubeID == "" {
		return nil, TextResult{}, errors.New("cube_id is required")
	}
	userName := input.UserID
	if userName == "" {
		userName = input.CubeID
	}
	deleted, err := pg.DeleteAllByUser(ctx, userName)
	if err != nil {
		return nil, TextResult{}, fmt.Errorf("delete_all_memories failed: %w", err)
	}
	// Purge ALL Qdrant preference vectors for this user to prevent ghost memories.
	if qd != nil {
		for _, coll := range prefCollections {
			if err := qd.PurgeByUserID(ctx, coll, userName); err != nil {
				logger.Warn("mcp delete_all: qdrant purge failed", slog.String("collection", coll), slog.Any("error", err))
			}
		}
	}
	return nil, TextResult{Result: map[string]any{"cube_id": input.CubeID, "deleted_count": deleted}}, nil
}
