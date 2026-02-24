// MemDB MCP Server — Streamable HTTP + STDIO transport.
//
// Exposes MemDB memory operations as MCP tools.
// Search is proxied to memdb-go (which runs ONNX locally).
// Memory CRUD and user tools use Postgres directly.
//
// Usage:
//   memdb-mcp          # HTTP mode on MEMDB_MCP_PORT (default 8001)
//   memdb-mcp --stdio  # STDIO mode for direct integration (e.g. Claude Code)
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/config"
	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/mcptools"
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

	// Postgres (memory CRUD and user tools) + Qdrant (preference cleanup on delete).
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
		// Fallback: use python backend if memdb-go URL not set.
		memdbGoURL = cfg.PythonBackendURL
		logger.Warn("MEMDB_GO_URL not set, search will proxy to python backend")
	}
	mcptools.RegisterSearchTool(server, memdbGoURL, cfg.InternalServiceSecret, logger)
	mcptools.RegisterMemoryTools(server, pg, qd, logger)
	mcptools.RegisterUserTools(server, pg, logger)
	mcptools.RegisterProxyTools(server, cfg.PythonBackendURL, cfg.InternalServiceSecret, logger)

	const mcpNativeToolCount = 6  // search + get/update/delete/delete_all + users (get_user_info, create_user)
	const mcpProxyToolCount  = 10 // add_memory, chat, create_cube, register_cube, unregister_cube, etc.
	logger.Info("MCP tools registered", slog.Int("native", mcpNativeToolCount), slog.Int("proxy", mcpProxyToolCount))

	if stdioMode {
		runStdio(ctx, server, logger, pg)
	} else {
		runHTTP(ctx, server, port, logger, pg)
	}
}

// ---------------------------------------------------------------------------
// STDIO transport
// ---------------------------------------------------------------------------

func runStdio(ctx context.Context, server *mcp.Server, logger *slog.Logger, pg *db.Postgres) {
	logger.Info("running in STDIO mode")

	sigCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	session, err := server.Connect(sigCtx, &mcp.StdioTransport{}, nil)
	if err != nil {
		logger.Error("stdio connect failed", slog.Any("error", err))
		cancel()
		os.Exit(1) //nolint:gocritic // cancel() already called explicitly above
	}

	if err := session.Wait(); err != nil {
		logger.Info("stdio session ended", slog.Any("reason", err))
	} else {
		logger.Info("stdio session ended")
	}

	cleanup(pg)
}

// ---------------------------------------------------------------------------
// HTTP transport (default)
// ---------------------------------------------------------------------------

func runHTTP(ctx context.Context, server *mcp.Server, port string, logger *slog.Logger, pg *db.Postgres) {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"memdb-mcp","version":"1.0.0"}`))
	})

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("MCP server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	<-sigCtx.Done()
	logger.Info("shutdown signal received")

	const shutdownTimeout = 15 * time.Second
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", slog.Any("error", err))
	}

	cleanup(pg)
	logger.Info("memdb-mcp shut down")
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func cleanup(pg *db.Postgres) {
	if pg != nil {
		pg.Close()
	}
}

func hasFlag(name string) bool {
	for _, arg := range os.Args[1:] {
		if arg == name {
			return true
		}
	}
	return false
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
