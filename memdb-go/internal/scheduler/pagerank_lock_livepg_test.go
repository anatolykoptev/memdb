//go:build livepg

package scheduler

// pagerank_lock_livepg_test.go — integration tests for the PageRank advisory lock.
//
// These tests require a real Postgres instance (MEMDB_LIVE_PG_DSN env var).
// They verify the core HA property: exactly one connection holds the lock at a
// time, and the lock is returned to the pool after explicit release.
//
// Run with:
//
//	MEMDB_LIVE_PG_DSN=<dsn> GOWORK=off go test -tags livepg ./internal/scheduler/...

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

// TestPagerankAdvisoryLock_AcquireRelease verifies the basic acquire→contend→release
// cycle against a live Postgres instance:
//  1. First connection acquires the lock — must return true.
//  2. A second independent Postgres client (different pool/session) tries to
//     acquire — must return false (lock is already held).
//  3. First connection releases — second attempt on the same second client now
//     succeeds (returns true), confirming the lock was actually freed.
func TestPagerankAdvisoryLock_AcquireRelease(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Open two independent Postgres clients (separate pgxpool.Pool instances)
	// to simulate two separate pod sessions.
	pg1 := openLivePG(ctx, t, logger)
	defer pg1.Pool().Close()

	pg2 := openLivePG(ctx, t, logger)
	defer pg2.Pool().Close()

	// Step 1: first client acquires the lock.
	locked1, err := tryAcquirePagerankAdvisoryLock(ctx, pg1)
	if err != nil {
		t.Fatalf("pg1 acquire: unexpected error: %v", err)
	}
	if !locked1 {
		// Another process may be holding the lock from a previous failed test.
		// Release on pg1 and retry once.
		_ = releasePagerankAdvisoryLock(ctx, pg1)
		locked1, err = tryAcquirePagerankAdvisoryLock(ctx, pg1)
		if err != nil {
			t.Fatalf("pg1 acquire retry: %v", err)
		}
		if !locked1 {
			t.Fatal("pg1: could not acquire advisory lock even after releasing it — is another process holding it?")
		}
	}

	// Step 2: second client (different session) must NOT acquire the lock.
	locked2, err := tryAcquirePagerankAdvisoryLock(ctx, pg2)
	if err != nil {
		// Release pg1 before failing so we don't leak.
		_ = releasePagerankAdvisoryLock(ctx, pg1)
		t.Fatalf("pg2 acquire: unexpected error: %v", err)
	}
	if locked2 {
		_ = releasePagerankAdvisoryLock(ctx, pg1)
		_ = releasePagerankAdvisoryLock(ctx, pg2)
		t.Fatal("pg2: acquired the lock while pg1 still holds it — advisory lock is not exclusive")
	}

	// Step 3: release from pg1, then pg2 must succeed.
	if err := releasePagerankAdvisoryLock(ctx, pg1); err != nil {
		t.Fatalf("pg1 release: %v", err)
	}

	locked2Again, err := tryAcquirePagerankAdvisoryLock(ctx, pg2)
	if err != nil {
		t.Fatalf("pg2 acquire after release: %v", err)
	}
	if !locked2Again {
		t.Fatal("pg2: failed to acquire lock after pg1 released it")
	}

	// Cleanup: release pg2's lock.
	_ = releasePagerankAdvisoryLock(ctx, pg2)
}
