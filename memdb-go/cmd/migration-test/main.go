// Package main — migration-test: invokes RunMigrations against a target DSN,
// intended for fresh-DB bootstrap verification. See scripts/test-migrations-fresh-db.sh.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dsn := os.Getenv("MEMDB_TEST_DSN")
	if dsn == "" {
		fatal("MEMDB_TEST_DSN env required, e.g. postgres://memos:test@localhost:55432/memos?sslmode=disable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fatal("pgxpool.New: %v", err)
	}
	defer pool.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	pg := db.NewTestPostgres(pool, logger)

	if err := pg.RunMigrations(ctx); err != nil {
		fatal("RunMigrations: %v", err)
	}
	fmt.Println("OK: RunMigrations completed")
}

func fatal(msg string, args ...any) {
	fmt.Fprintf(os.Stderr, "migration-test: "+msg+"\n", args...)
	os.Exit(1)
}
