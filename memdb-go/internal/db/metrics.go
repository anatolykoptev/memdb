// Package db — domain metrics for db-level instrumentation.
// Instruments are lazily initialised and read by the Prometheus exporter.
package db

import (
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	dbMetricsOnce sync.Once
	dbMetrics     *dbMetricsInstruments
)

type dbMetricsInstruments struct {
	// MigrationChecksumDrift counts how many times the runner detected that
	// an already-applied migration's sha256 differs from the embedded bytes.
	// A non-zero value means ops must investigate — a migration file was
	// edited after being applied. Labelled by migration name.
	MigrationChecksumDrift metric.Int64Counter

	// MemoryAdded counts successful inserts into memos_graph."Memory" by type and cube_id.
	// Emitted after InsertMemoryNodes commits the transaction. A stalled rate
	// (0 for 1h) means the memory-write pipeline is silently broken.
	MemoryAdded metric.Int64Counter
}

func dbMx() *dbMetricsInstruments {
	dbMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/db")
		drift, _ := meter.Int64Counter("memdb.migration.checksum_drift",
			metric.WithDescription("Count of migration checksum mismatches detected at startup (by migration name). Non-zero means an applied file was edited — manual intervention required."),
		)
		added, _ := meter.Int64Counter("memdb.memory.added",
			metric.WithDescription("Count of memory nodes successfully inserted into memos_graph.Memory, labelled by type and cube_id. Stall (rate=0 for 1h) indicates pipeline breakage."),
		)
		dbMetrics = &dbMetricsInstruments{
			MigrationChecksumDrift: drift,
			MemoryAdded:            added,
		}
	})
	return dbMetrics
}
