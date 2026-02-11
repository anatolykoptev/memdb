package mcptools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/MemDBai/MemDB/memdb-go/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterSearchTool registers the search_memories MCP tool.
func RegisterSearchTool(server *mcp.Server, svc *search.SearchService, logger *slog.Logger) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_memories",
		Description: "Perform semantic search through memories in accessible cubes. Returns text_mem, skill_mem, pref_mem, and tool_mem categories.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, TextResult, error) {
		if input.Query == "" {
			return nil, TextResult{}, fmt.Errorf("query is required")
		}

		userName := input.UserID
		if userName == "" {
			userName = "memos"
		}
		cubeID := userName
		if len(input.CubeIDs) > 0 {
			cubeID = input.CubeIDs[0]
		}

		topK := search.DefaultTextTopK
		if input.TopK > 0 {
			topK = input.TopK
		}
		relativity := 0.85
		if input.Relativity > 0 {
			relativity = input.Relativity
		}
		dedup := "mmr"
		if input.Dedup != "" {
			dedup = input.Dedup
		}

		output, err := svc.Search(ctx, search.SearchParams{
			Query:        input.Query,
			UserName:     userName,
			CubeID:       cubeID,
			TopK:         topK,
			SkillTopK:    search.DefaultSkillTopK,
			PrefTopK:     search.DefaultPrefTopK,
			ToolTopK:     search.DefaultToolTopK,
			Dedup:        dedup,
			Relativity:   relativity,
			IncludeSkill: true,
			IncludePref:  true,
			IncludeTool:  true,
		})
		if err != nil {
			return nil, TextResult{}, fmt.Errorf("search failed: %w", err)
		}

		return nil, TextResult{Result: output.Result}, nil
	})
}
