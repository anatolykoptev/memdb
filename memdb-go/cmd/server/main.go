// MemDB Go API Server — Phase 1: Reverse Proxy to Python Backend.
//
// This is the entry point for the Go API gateway. It:
// 1. Loads config from environment variables
// 2. Sets up structured logging with slog
// 3. Initializes OpenTelemetry (if enabled)
// 4. Starts the HTTP server with all routes proxying to Python
// 5. Handles graceful shutdown on SIGINT/SIGTERM
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/MemDBai/MemDB/memdb-go/internal/config"
	"github.com/MemDBai/MemDB/memdb-go/internal/server"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
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

	// Initialize OpenTelemetry if enabled
	var otelShutdown func(context.Context) error
	if cfg.OTelEnabled {
		var err error
		otelShutdown, err = initOTel(cfg)
		if err != nil {
			logger.Warn("otel init failed, continuing without telemetry", slog.Any("error", err))
			cfg.OTelEnabled = false
		} else {
			logger.Info("opentelemetry initialized",
				slog.String("endpoint", cfg.OTelEndpoint),
				slog.String("service", cfg.OTelServiceName),
			)
		}
	}

	// Create HTTP server
	srv, cleanup := server.New(cfg, logger)
	defer cleanup()

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
		logger.Error("server shutdown error", slog.Any("error", err))
	} else {
		logger.Info("server shut down gracefully")
	}

	// Shutdown OTel providers (flush pending data)
	if otelShutdown != nil {
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error("otel shutdown error", slog.Any("error", err))
		}
	}
}

// initOTel sets up OpenTelemetry trace and metric providers with OTLP HTTP exporters.
func initOTel(cfg *config.Config) (func(context.Context) error, error) {
	ctx := context.Background()

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(cfg.OTelServiceName),
		),
	)
	if err != nil {
		return nil, err
	}

	// Trace exporter
	traceOpts := []otlptracehttp.Option{
		otlptracehttp.WithInsecure(),
	}
	if cfg.OTelEndpoint != "" {
		traceOpts = append(traceOpts, otlptracehttp.WithEndpoint(cfg.OTelEndpoint))
	}
	traceExporter, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// Metric exporter
	metricOpts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithInsecure(),
	}
	if cfg.OTelEndpoint != "" {
		metricOpts = append(metricOpts, otlpmetrichttp.WithEndpoint(cfg.OTelEndpoint))
	}
	metricExporter, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		tp.Shutdown(ctx)
		return nil, err
	}

	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
		metric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	shutdown := func(ctx context.Context) error {
		if err := tp.Shutdown(ctx); err != nil {
			return err
		}
		return mp.Shutdown(ctx)
	}
	return shutdown, nil
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
