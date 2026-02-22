package mcptools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// defaultUserID is the fallback user identifier used when no user_id is provided.
const defaultUserID = "memos"

// RegisterUserTools registers create_user and get_user_info MCP tools.
func RegisterUserTools(server *mcp.Server, pg *db.Postgres, logger *slog.Logger) {
	// create_user — stub (MemDB auto-creates users on first add)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_user",
		Description: "Create a new user account with specified role (USER or ADMIN). MemDB auto-creates users on first memory add.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input CreateUserInput) (*mcp.CallToolResult, TextResult, error) {
		if input.UserID == "" {
			return nil, TextResult{}, errors.New("user_id is required")
		}

		role := input.Role
		if role == "" {
			role = "USER"
		}

		return nil, TextResult{Result: map[string]any{
			"user_id":    input.UserID,
			"role":       role,
			"registered": true,
		}}, nil
	})

	// get_user_info
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_user_info",
		Description: "Get user information and list of accessible memory cubes.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetUserInfoInput) (*mcp.CallToolResult, TextResult, error) {
		userID := input.UserID
		if userID == "" {
			userID = defaultUserID
		}

		exists, err := pg.ExistUser(ctx, userID)
		if err != nil {
			return nil, TextResult{}, fmt.Errorf("get_user_info failed: %w", err)
		}

		users, _ := pg.ListUsers(ctx)
		cubes := []string{}
		for _, u := range users {
			if u == userID {
				cubes = append(cubes, u)
			}
		}

		return nil, TextResult{Result: map[string]any{
			"user_id":          userID,
			"exists":           exists,
			"accessible_cubes": cubes,
		}}, nil
	})
}
