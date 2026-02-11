// MemDB MCP Server — Streamable HTTP transport.
//
// Exposes MemDB memory operations as MCP tools, reusing the same
// internal packages (db, embedder, search) as the REST API gateway.
//
// Native tools: search_memories, get_memory, update_memory, delete_memory,
//   delete_all_memories, create_user, get_user_info (6 native + 2 new SQL)
// Proxied tools: add_memory, chat, clear_chat_history, cube mgmt, scheduler
//   (9 tools forwarded to Python backend)
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
	"github.com/MemDBai/MemDB/memdb-go/internal/embedder"
	"github.com/MemDBai/MemDB/memdb-go/internal/mcptools"
	"github.com/MemDBai/MemDB/memdb-go/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	cfg := config.Load()

	// Override port for MCP server (default 8001, separate from REST gateway on 8080)
	port := os.Getenv("MEMDB_MCP_PORT")
	if port == "" {
		port = "8001"
	}

	// Logging
	var logHandler slog.Handler
	opts := &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}
	if cfg.LogFormat == "json" {
		logHandler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		logHandler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(logHandler)
	slog.SetDefault(logger)

	logger.Info("starting memdb-mcp",
		slog.String("port", port),
		slog.String("python_backend", cfg.PythonBackendURL),
	)

	ctx := context.Background()

	// Initialize DB clients
	var pg *db.Postgres
	if cfg.PostgresURL != "" {
		var err error
		pg, err = db.NewPostgres(ctx, cfg.PostgresURL, logger)
		if err != nil {
			logger.Error("postgres init failed", slog.Any("error", err))
			os.Exit(1) // MCP server requires postgres for native tools
		}
	}

	var qd *db.Qdrant
	if cfg.QdrantAddr != "" {
		var err error
		qd, err = db.NewQdrant(ctx, cfg.QdrantAddr, logger)
		if err != nil {
			logger.Warn("qdrant unavailable, pref search disabled", slog.Any("error", err))
		}
	}

	// Initialize embedder (same logic as REST API gateway)
	var emb embedder.Embedder
	switch cfg.EmbedderType {
	case "voyage":
		if cfg.VoyageAPIKey != "" {
			emb = embedder.NewVoyageClient(cfg.VoyageAPIKey, cfg.EmbedderModel, logger)
			logger.Info("voyage embedder initialized", slog.String("model", cfg.EmbedderModel))
		} else {
			logger.Error("VOYAGE_API_KEY not set for voyage embedder")
			os.Exit(1)
		}
	default: // "onnx"
		if cfg.ONNXModelDir != "" {
			onnxEmb, err := embedder.NewONNXEmbedder(cfg.ONNXModelDir, logger)
			if err != nil {
				logger.Error("onnx embedder init failed", slog.Any("error", err))
				os.Exit(1)
			}
			emb = onnxEmb
			logger.Info("onnx embedder initialized",
				slog.String("model_dir", cfg.ONNXModelDir),
				slog.Int("dimension", onnxEmb.Dimension()))
		} else {
			logger.Error("MEMDB_ONNX_MODEL_DIR not set")
			os.Exit(1)
		}
	}

	// Create MCP server
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "memdb-mcp",
		Version: "1.0.0",
	}, nil)

	// Create search service
	searchSvc := search.NewSearchService(pg, qd, emb, logger)

	// Register native tools
	mcptools.RegisterSearchTool(server, searchSvc, logger)
	mcptools.RegisterMemoryTools(server, pg, logger)
	mcptools.RegisterUserTools(server, pg, logger)

	// Register proxy tools (forwarded to Python backend)
	mcptools.RegisterProxyTools(server, cfg.PythonBackendURL, cfg.InternalServiceSecret, logger)

	logger.Info("MCP tools registered",
		slog.Int("native", 7),  // search + get + update + delete + delete_all + create_user + get_user_info
		slog.Int("proxy", 9),   // add, chat, clear_chat, create_cube, register_cube, unregister_cube, share_cube, dump_cube, scheduler
	)

	// Create Streamable HTTP handler
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{
		Stateless: true, // No session state needed
	})

	// Health check endpoint alongside MCP
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"memdb-mcp","version":"1.0.0"}`))
	})

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	// Graceful shutdown
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", slog.Any("error", err))
	}

	// Cleanup
	if emb != nil {
		emb.Close()
	}
	if pg != nil {
		pg.Close()
	}
	if qd != nil {
		qd.Close()
	}

	logger.Info("memdb-mcp shut down")
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
