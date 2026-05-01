// Package pgxotel wires the canonical exaring/otelpgx tracer onto pgx pool
// configs. Each SQL query becomes a span with db.statement, db.system,
// and db.operation attributes; pool waits become events.
//
// pgx is the de-facto Postgres driver in our fleet (memdb-go, go-nerv,
// oxpulse-chat). otelpgx is the production-grade tracer for it (211 stars,
// active, supports pgx v5 + pgxpool stats).
//
// We deliberately don't ship database/sql wrapping — use XSAM/otelsql
// directly when needed. Connect RPC instrumentation lives in
// connectrpc/otelconnect upstream.
//
// Usage:
//
//	cfg, _ := pgxpool.ParseConfig(databaseURL)
//	pgxotel.AttachTracer(cfg)
//	pool, _ := pgxpool.NewWithConfig(ctx, cfg)
//
// Once attached, every pool.Query / Exec / SendBatch creates a span whose
// parent is the span in the calling context. Combined with the server-side
// HTTP/MCP middleware, you see the full chain handler → DB query latency
// in Jaeger.
package pgxotel

import (
	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AttachTracer mounts an otelpgx tracer on cfg.ConnConfig.Tracer.
//
// Mutates cfg in place AND returns it for fluent chaining:
//
//	pool, _ := pgxpool.NewWithConfig(ctx, pgxotel.AttachTracer(cfg))
//
// Idempotent — calling twice replaces the previously attached tracer.
//
// Tracer config: query parameters NOT included by default to keep span
// payloads small and avoid leaking secrets via embedded query strings.
// Pass otelpgx.WithIncludeQueryParameters() via Options if you want them.
func AttachTracer(cfg *pgxpool.Config, opts ...otelpgx.Option) *pgxpool.Config {
	if cfg == nil {
		return nil
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer(opts...)
	return cfg
}

// RecordPoolStats periodically reports pgxpool internal stats (connections
// active/idle/total, acquire counts, queue depth) as OTel metrics. Pass
// the pool you got from pgxpool.NewWithConfig and a context that controls
// the lifetime — cancel ctx to stop the reporter.
//
// Returns the canonical otelpgx.RecordStats. Wrapped here only to keep
// the import surface in one place for our services.
func RecordPoolStats(pool *pgxpool.Pool, opts ...otelpgx.StatsOption) error {
	return otelpgx.RecordStats(pool, opts...)
}
