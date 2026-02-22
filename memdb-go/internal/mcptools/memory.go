package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterMemoryTools registers get_memory, update_memory, delete_memory, and delete_all_memories.
func RegisterMemoryTools(server *mcp.Server, pg *db.Postgres, logger *slog.Logger) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_memory",
		Description: "Retrieve a specific memory by its unique identifier from a memory cube.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input GetMemoryInput) (*mcp.CallToolResult, TextResult, error) {
		return handleGetMemory(ctx, pg, input)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_memory",
		Description: "Update existing memory content while preserving metadata.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input UpdateMemoryInput) (*mcp.CallToolResult, TextResult, error) {
		return handleUpdateMemory(ctx, pg, input)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_memory",
		Description: "Permanently delete a specific memory from a cube.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DeleteMemoryInput) (*mcp.CallToolResult, TextResult, error) {
		return handleDeleteMemory(ctx, pg, input)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_all_memories",
		Description: "Permanently delete all memories from a specific cube. Use with caution.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input DeleteAllMemoriesInput) (*mcp.CallToolResult, TextResult, error) {
		return handleDeleteAllMemories(ctx, pg, input)
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

func handleUpdateMemory(ctx context.Context, pg *db.Postgres, input UpdateMemoryInput) (*mcp.CallToolResult, TextResult, error) {
	if input.MemoryID == "" {
		return nil, TextResult{}, errors.New("memory_id is required")
	}
	if input.MemoryContent == "" {
		return nil, TextResult{}, errors.New("memory_content is required")
	}
	updated, err := pg.UpdateMemoryContent(ctx, input.MemoryID, input.MemoryContent)
	if err != nil {
		return nil, TextResult{}, fmt.Errorf("update_memory failed: %w", err)
	}
	return nil, TextResult{Result: map[string]any{"memory_id": input.MemoryID, "updated": updated}}, nil
}

func handleDeleteMemory(ctx context.Context, pg *db.Postgres, input DeleteMemoryInput) (*mcp.CallToolResult, TextResult, error) {
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
	return nil, TextResult{Result: map[string]any{"memory_id": input.MemoryID, "deleted_count": deleted}}, nil
}

func handleDeleteAllMemories(ctx context.Context, pg *db.Postgres, input DeleteAllMemoriesInput) (*mcp.CallToolResult, TextResult, error) {
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
	return nil, TextResult{Result: map[string]any{"cube_id": input.CubeID, "deleted_count": deleted}}, nil
}
