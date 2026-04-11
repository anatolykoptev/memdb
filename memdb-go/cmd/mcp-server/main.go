// MemDB MCP Server — Streamable HTTP + STDIO transport.
//
// Exposes MemDB memory operations as MCP tools.
// Search is proxied to memdb-go (which runs ONNX locally).
// Memory CRUD and user tools use Postgres directly.
//
// Usage:
//
//	memdb-mcp          # HTTP mode on MEMDB_MCP_PORT (default 8001)
//	memdb-mcp --stdio  # STDIO mode for direct integration (e.g. Claude Code)
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/anatolykoptev/memdb/memdb-go/internal/config"
	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/mcptools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	stdioMode := hasFlag("--stdio")
	cfg := config.Load()

	port := os.Getenv("MEMDB_MCP_PORT")
	if port == "" {
		port = "8001"
	}

	// In STDIO mode all logs go to stderr so stdout stays clean for JSON-RPC.
	logDst := os.Stdout
	if stdioMode {
		logDst = os.Stderr
	}

	var logHandler slog.Handler
	opts := &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}
	if cfg.LogFormat == "json" {
		logHandler = slog.NewJSONHandler(logDst, opts)
	} else {
		logHandler = slog.NewTextHandler(logDst, opts)
	}
	logger := slog.New(logHandler)
	slog.SetDefault(logger)

	logger.Info("starting memdb-mcp",
		slog.String("port", port),
		slog.String("python_backend", cfg.PythonBackendURL),
		slog.String("memdb_go_url", cfg.MemDBGoURL),
		slog.Bool("stdio", stdioMode),
	)

	ctx := context.Background()

	// Postgres (memory CRUD, user, and cube tools) + Qdrant (preference cleanup on delete).
	var pg *db.Postgres
	if cfg.PostgresURL != "" {
		var err error
		pg, err = db.NewPostgres(ctx, cfg.PostgresURL, logger)
		if err != nil {
			logger.Error("postgres init failed", slog.Any("error", err))
			os.Exit(1)
		}
	}

	var qd *db.Qdrant
	if cfg.QdrantAddr != "" {
		var err error
		qd, err = db.NewQdrant(ctx, cfg.QdrantAddr, logger)
		if err != nil {
			logger.Warn("qdrant init failed (preference cleanup disabled)", slog.Any("error", err))
		}
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "memdb-mcp",
		Version: "1.0.0",
	}, nil)

	// Search is proxied to memdb-go (which has the ONNX embedder).
	memdbGoURL := cfg.MemDBGoURL
	if memdbGoURL == "" {
		memdbGoURL = cfg.PythonBackendURL
		logger.Warn("MEMDB_GO_URL not set, search will proxy to python backend")
	}
	mcptools.RegisterSearchTool(server, memdbGoURL, cfg.InternalServiceSecret, logger)
	mcptools.RegisterMemoryTools(server, pg, qd, logger)
	mcptools.RegisterUserTools(server, pg, logger)
	mcptools.RegisterCubeTools(server, pg, logger)
	mcptools.RegisterNativeGoProxyTools(server, memdbGoURL, cfg.InternalServiceSecret, logger)
	mcptools.RegisterPythonProxyTools(server, cfg.PythonBackendURL, cfg.InternalServiceSecret, logger)

	const mcpNativeToolCount = 10     // search + memory CRUD + users + cubes (create/list/delete/get_user_cubes)
	const mcpGoProxyToolCount = 3     // add_memory, chat, clear_chat_history → memdb-go native backend
	const mcpPythonProxyToolCount = 1 // control_memory_scheduler → Python legacy
	logger.Info("MCP tools registered",
		slog.Int("native", mcpNativeToolCount),
		slog.Int("go_proxy", mcpGoProxyToolCount),
		slog.Int("python_proxy", mcpPythonProxyToolCount),
	)

	if stdioMode {
		runStdio(ctx, server, logger, pg)
	} else {
		runHTTP(ctx, server, port, logger, pg)
	}
}
