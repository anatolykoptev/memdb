package mcptools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterSearchTool registers the search_memories MCP tool.
// It proxies to memdb-go /product/search instead of running ONNX locally.
func RegisterSearchTool(server *mcp.Server, memdbGoURL string, serviceSecret string, logger *slog.Logger) {
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

		topK := 6
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

		proxyInput := SearchMemoriesProxyInput{
			Query:      input.Query,
			UserID:     userName,
			TopK:       topK,
			Relativity: relativity,
			Dedup:      dedup,
			CubeIDs:    input.CubeIDs,
		}

		result, err := proxyCall(ctx, memdbGoURL, "/product/search", serviceSecret, "search_memories", proxyInput, logger)
		if err != nil {
			return nil, TextResult{}, fmt.Errorf("search failed: %w", err)
		}

		return nil, result, nil
	})
}
