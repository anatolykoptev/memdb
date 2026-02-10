// MemDB Go API Server — Phase 1: Reverse Proxy to Python Backend.
//
// This is the entry point for the Go API gateway. It:
// 1. Loads config from environment variables
// 2. Sets up structured logging with slog
// 3. Starts the HTTP server with all routes proxying to Python
// 4. Handles graceful shutdown on SIGINT/SIGTERM
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/memtensor/memdb-go/internal/config"
	"github.com/memtensor/memdb-go/internal/server"
)

func main() {
	// Load configuration
	cfg := config.Load()

	// Set up structured logging with slog
	var logHandler slog.Handler
	opts := &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
	}
	if cfg.LogFormat == "json" {
		logHandler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		logHandler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(logHandler)
	slog.SetDefault(logger)

	logger.Info("starting memdb-go",
		slog.String("config", cfg.String()),
	)

	// Create HTTP server
	srv := server.New(cfg, logger)

	// Graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start server in goroutine
	go func() {
		logger.Info("server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutdown signal received, draining connections...")

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", slog.Any("error", err))
	} else {
		logger.Info("server shut down gracefully")
	}
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
