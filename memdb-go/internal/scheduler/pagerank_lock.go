package scheduler

// pagerank_lock.go — Postgres advisory lock helpers for multi-replica HA gate.
//
// In a Helm deployment with replicaCount > 1 every replica runs runPageRankLoop
// independently. Without coordination all N replicas would issue concurrent bulk
// UPDATEs on the same rows every 6 h. Advisory locks elect exactly one leader per
// Postgres session: the first replica to call pg_try_advisory_lock wins; the
// others see false and skip with outcome=skipped_other_leader.
//
// Lock semantics:
//   - Session-scoped: auto-released on connection close / pod crash.
//   - Non-blocking: pg_try_advisory_lock returns immediately (no queue).
//   - Explicit release via pg_advisory_unlock in defer (best practice).
//
// The lock key is a fixed int64 derived from the ASCII bytes of "MEMDB_PR",
// deterministic across replicas and Postgres major versions.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// pagerankAdvisoryLockKey is the fixed session-level advisory lock identifier
// shared across all replicas. Value = big-endian int64 for ASCII "MEMDB_PR".
const pagerankAdvisoryLockKey int64 = 0x4D454D44425F5052

// tryAcquirePagerankAdvisoryLock attempts a non-blocking Postgres session
// advisory lock. Returns (true, nil) when this replica wins the lock,
// (false, nil) when another replica already holds it, or (false, err) on a
// database error.
func tryAcquirePagerankAdvisoryLock(ctx context.Context, pg *db.Postgres) (bool, error) {
	var locked bool
	err := pg.Pool().QueryRow(ctx,
		"SELECT pg_try_advisory_lock($1)", pagerankAdvisoryLockKey,
	).Scan(&locked)
	if err != nil {
		return false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	return locked, nil
}

// releasePagerankAdvisoryLock releases the session advisory lock acquired by
// tryAcquirePagerankAdvisoryLock. Intended for use in a defer — errors are
// logged but not propagated (the lock auto-releases on session close anyway).
func releasePagerankAdvisoryLock(ctx context.Context, pg *db.Postgres) error {
	var released bool
	err := pg.Pool().QueryRow(ctx,
		"SELECT pg_advisory_unlock($1)", pagerankAdvisoryLockKey,
	).Scan(&released)
	if err != nil {
		slog.Default().Warn("pagerank: advisory unlock failed", "err", err)
		return err
	}
	if !released {
		slog.Default().Warn("pagerank: advisory unlock returned false (lock was not held by this session)")
	}
	return nil
}
