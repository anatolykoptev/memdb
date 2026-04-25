package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
		_, _ = w.Write([]byte(`{"status":"ok","service":"memdb-mcp","version":"0.22.0"}`))
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
