package mcptools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpDefaultTopK      = 6
	mcpDefaultRelativity = 0.85
	mcpDefaultDedup     = "mmr"
)

// buildProxySearchInput constructs the proxy payload from MCP search input.
// In profile mode the server handles defaults; otherwise MCP backward-compat defaults apply.
func buildProxySearchInput(input SearchInput) SearchMemoriesProxyInput {
	userName := input.UserID
	if userName == "" {
		userName = defaultUserID
	}
	p := SearchMemoriesProxyInput{
		Query:   input.Query,
		UserID:  userName,
		CubeIDs: input.CubeIDs,
	}
	if input.Profile != "" {
		p.Profile = input.Profile
		if input.TopK > 0 {
			p.TopK = input.TopK
		}
		if input.Relativity > 0 {
			p.Relativity = input.Relativity
		}
		if input.Dedup != "" {
			p.Dedup = input.Dedup
		}
		return p
	}
	// No profile: use MCP defaults for backward compatibility.
	p.TopK = mcpDefaultTopK
	if input.TopK > 0 {
		p.TopK = input.TopK
	}
	p.Relativity = mcpDefaultRelativity
	if input.Relativity > 0 {
		p.Relativity = input.Relativity
	}
	p.Dedup = mcpDefaultDedup
	if input.Dedup != "" {
		p.Dedup = input.Dedup
	}
	return p
}

// RegisterSearchTool registers the search_memories MCP tool.
// It proxies to memdb-go /product/search instead of running ONNX locally.
func RegisterSearchTool(server *mcp.Server, memdbGoURL string, serviceSecret string, logger *slog.Logger) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_memories",
		Description: "Perform semantic search through memories in accessible cubes. Returns text_mem, skill_mem, pref_mem, and tool_mem categories.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
		},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, TextResult, error) {
		if input.Query == "" {
			return nil, TextResult{}, errors.New("query is required")
		}
		proxyInput := buildProxySearchInput(input)
		result, err := proxyCall(ctx, memdbGoURL, "/product/search", serviceSecret, "search_memories", proxyInput, logger)
		if err != nil {
			return nil, TextResult{}, fmt.Errorf("search failed: %w", err)
		}
		return nil, result, nil
	})
}
