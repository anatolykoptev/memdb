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

// proxyCall sends a JSON-encoded request to the Python backend and returns the result.
func proxyCall(ctx context.Context, pythonURL string, endpoint string, serviceSecret string, toolName string, input any, logger *slog.Logger) (TextResult, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return TextResult{}, fmt.Errorf("marshal error: %w", err)
	}

	url := pythonURL + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return TextResult{}, fmt.Errorf("request error: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if serviceSecret != "" {
		httpReq.Header.Set("X-Internal-Service", serviceSecret)
	}

	resp, err := proxyClient.Do(httpReq)
	if err != nil {
		return TextResult{}, fmt.Errorf("proxy request to %s failed: %w", toolName, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return TextResult{}, fmt.Errorf("read response error: %w", err)
	}

	if resp.StatusCode >= 400 {
		return TextResult{}, fmt.Errorf("%s returned HTTP %d: %s", toolName, resp.StatusCode, truncate(string(respBody), 200))
	}

	var result any
	if json.Unmarshal(respBody, &result) != nil {
		result = string(respBody)
	}

	logger.Debug("proxy tool call",
		slog.String("tool", toolName),
		slog.Int("status", resp.StatusCode),
	)

	return TextResult{Result: result}, nil
}

// RegisterProxyTools registers MCP tools that proxy to the Python backend.
func RegisterProxyTools(server *mcp.Server, pythonURL string, serviceSecret string, logger *slog.Logger) {
	// add_memory
	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_memory",
		Description: "Add memories from text content, document files, or conversation messages. (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input AddMemoryProxyInput) (*mcp.CallToolResult, TextResult, error) {
		result, err := proxyCall(ctx, pythonURL, "/product/add", serviceSecret, "add_memory", input, logger)
		return nil, result, err
	})

	// chat
	mcp.AddTool(server, &mcp.Tool{
		Name:        "chat",
		Description: "Chat with MemDB system using memory-enhanced responses with semantic search. (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ChatProxyInput) (*mcp.CallToolResult, TextResult, error) {
		result, err := proxyCall(ctx, pythonURL, "/product/chat/complete", serviceSecret, "chat", input, logger)
		return nil, result, err
	})

	// clear_chat_history
	mcp.AddTool(server, &mcp.Tool{
		Name:        "clear_chat_history",
		Description: "Reset conversation history while keeping memory cubes and stored memories intact. (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ClearChatHistoryProxyInput) (*mcp.CallToolResult, TextResult, error) {
		result, err := proxyCall(ctx, pythonURL, "/product/chat/complete", serviceSecret, "clear_chat_history", input, logger)
		return nil, result, err
	})

	// create_cube
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_cube",
		Description: "Create a new memory cube for a user to store different types of memories. (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input CreateCubeProxyInput) (*mcp.CallToolResult, TextResult, error) {
		result, err := proxyCall(ctx, pythonURL, "/product/configure", serviceSecret, "create_cube", input, logger)
		return nil, result, err
	})

	// register_cube
	mcp.AddTool(server, &mcp.Tool{
		Name:        "register_cube",
		Description: "Register an existing memory cube from file path or create new one. (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input RegisterCubeProxyInput) (*mcp.CallToolResult, TextResult, error) {
		result, err := proxyCall(ctx, pythonURL, "/product/configure", serviceSecret, "register_cube", input, logger)
		return nil, result, err
	})

	// unregister_cube
	mcp.AddTool(server, &mcp.Tool{
		Name:        "unregister_cube",
		Description: "Unregister a memory cube from the active session (data remains on disk). (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input UnregisterCubeProxyInput) (*mcp.CallToolResult, TextResult, error) {
		result, err := proxyCall(ctx, pythonURL, "/product/configure", serviceSecret, "unregister_cube", input, logger)
		return nil, result, err
	})

	// share_cube
	mcp.AddTool(server, &mcp.Tool{
		Name:        "share_cube",
		Description: "Grant access to a memory cube to another user for reading and searching. (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ShareCubeProxyInput) (*mcp.CallToolResult, TextResult, error) {
		result, err := proxyCall(ctx, pythonURL, "/product/configure", serviceSecret, "share_cube", input, logger)
		return nil, result, err
	})

	// dump_cube
	mcp.AddTool(server, &mcp.Tool{
		Name:        "dump_cube",
		Description: "Export a memory cube to a directory for backup or migration purposes. (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input DumpCubeProxyInput) (*mcp.CallToolResult, TextResult, error) {
		result, err := proxyCall(ctx, pythonURL, "/product/configure", serviceSecret, "dump_cube", input, logger)
		return nil, result, err
	})

	// control_memory_scheduler
	mcp.AddTool(server, &mcp.Tool{
		Name:        "control_memory_scheduler",
		Description: "Control the memory scheduler service (start/stop background processing). (proxied to Python backend)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ControlSchedulerProxyInput) (*mcp.CallToolResult, TextResult, error) {
		result, err := proxyCall(ctx, pythonURL, "/product/scheduler/wait", serviceSecret, "control_memory_scheduler", input, logger)
		return nil, result, err
	})
}

func truncate(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
