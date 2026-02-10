package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// proxyClient is a shared HTTP client for forwarding requests to Python.
var proxyClient = &http.Client{
	Timeout: 120 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
	},
}

// proxyTool definition for tools forwarded to Python.
type proxyToolDef struct {
	name        string
	description string
	endpoint    string
}

// RegisterProxyTools registers MCP tools that proxy to the Python backend.
func RegisterProxyTools(server *mcp.Server, pythonURL string, serviceSecret string, logger *slog.Logger) {
	proxyTools := []proxyToolDef{
		{
			name:        "add_memory",
			description: "Add memories from text content, document files, or conversation messages.",
			endpoint:    "/product/add",
		},
		{
			name:        "chat",
			description: "Chat with MemDB system using memory-enhanced responses with semantic search.",
			endpoint:    "/product/chat/complete",
		},
		{
			name:        "clear_chat_history",
			description: "Reset conversation history while keeping memory cubes and stored memories intact.",
			endpoint:    "/product/chat/complete", // uses special params
		},
		{
			name:        "create_cube",
			description: "Create a new memory cube for a user to store different types of memories.",
			endpoint:    "/product/configure",
		},
		{
			name:        "register_cube",
			description: "Register an existing memory cube from file path or create new one.",
			endpoint:    "/product/configure",
		},
		{
			name:        "unregister_cube",
			description: "Unregister a memory cube from the active session (data remains on disk).",
			endpoint:    "/product/configure",
		},
		{
			name:        "share_cube",
			description: "Grant access to a memory cube to another user for reading and searching.",
			endpoint:    "/product/configure",
		},
		{
			name:        "dump_cube",
			description: "Export a memory cube to a directory for backup or migration purposes.",
			endpoint:    "/product/configure",
		},
		{
			name:        "control_memory_scheduler",
			description: "Control the memory scheduler service (start/stop background processing).",
			endpoint:    "/product/scheduler/wait",
		},
	}

	for _, tool := range proxyTools {
		registerProxyTool(server, pythonURL, serviceSecret, tool, logger)
	}
}

func registerProxyTool(server *mcp.Server, pythonURL string, serviceSecret string, def proxyToolDef, logger *slog.Logger) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        def.name,
		Description: def.description + " (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ProxyInput) (*mcp.CallToolResult, TextResult, error) {
		body, err := json.Marshal(input)
		if err != nil {
			return nil, TextResult{}, fmt.Errorf("marshal error: %w", err)
		}

		url := pythonURL + def.endpoint
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, TextResult{}, fmt.Errorf("request error: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if serviceSecret != "" {
			httpReq.Header.Set("X-Internal-Service", serviceSecret)
		}

		resp, err := proxyClient.Do(httpReq)
		if err != nil {
			return nil, TextResult{}, fmt.Errorf("proxy request to %s failed: %w", def.name, err)
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, TextResult{}, fmt.Errorf("read response error: %w", err)
		}

		if resp.StatusCode >= 400 {
			return nil, TextResult{}, fmt.Errorf("%s returned HTTP %d: %s", def.name, resp.StatusCode, truncate(string(respBody), 200))
		}

		var result any
		if json.Unmarshal(respBody, &result) != nil {
			result = string(respBody)
		}

		logger.Debug("proxy tool call",
			slog.String("tool", def.name),
			slog.Int("status", resp.StatusCode),
		)

		return nil, TextResult{Result: result}, nil
	})
}

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
