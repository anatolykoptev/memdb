//go:build livepg

// nan_guard_livepg_test.go — E2E test for the NaN guard in isDuplicate.
//
// When a stored memory has a zero embedding (e.g. ONNX embedder fell back
// to zero on empty tokenization), pgvector <=> returns NaN for the cosine
// distance, and 1 - NaN = NaN. Without the NaN guard in isDuplicate,
// `NaN < threshold` is false in Go, so the loop treats the NaN-scored row
// as a duplicate and silently drops the insert.
//
// This test inserts a zero-vector memory directly into the DB (bypassing
// the source-layer guard in embedSingle), then verifies that isDuplicate
// returns false — the NaN guard must break out of the loop and treat the
// NaN-scored row as "not a match".

package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func TestLivePG_IsDuplicate_NaNGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := os.Getenv("MEMDB_LIVE_PG_DSN")
	if dsn == "" {
		t.Skip("MEMDB_LIVE_PG_DSN not set; skipping live-Postgres NaN guard test")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pg, err := db.NewPostgres(ctx, dsn, logger)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	defer pg.Pool().Close()

	cubeID := "test-nan-guard-" + uuid.New().String()
	t.Logf("livepg NaN guard test cube_id=%s", cubeID)

	// Insert a memory with a zero embedding directly into the DB,
	// bypassing embedSingle's isZeroVector guard. This simulates a
	// historical row that predates the source-layer fix.
	zeroVec := make([]float32, livepgEmbedDim)
	zeroVecStr := vecToPgVector(zeroVec)
	idExisting := uuid.New().String()
	props := fmt.Sprintf(`{"id":%q,"memory":"existing zero-vec memory","memory_type":"LongTermMemory","user_name":%q,"user_id":%q,"status":"activated"}`, idExisting, cubeID, cubeID)
	_, err = pg.Pool().Exec(ctx, `INSERT INTO memos_graph."Memory" (properties, embedding) VALUES ($1::agtype, $2::halfvec(1024))`, props, zeroVecStr)
	if err != nil {
		t.Fatalf("insert zero-vec memory: %v", err)
	}

	cleanup := func() {
		pg.Pool().Exec(ctx, `DELETE FROM memos_graph."Memory" WHERE properties->>(('user_name'::text)) = $1`, cubeID)
	}
	t.Cleanup(cleanup)
	defer cleanup()

	// Build a Handler with the livepgEmbedder (non-zero vectors) and
	// call isDuplicate with a fresh embedding. The zero-vec memory in
	// the DB will produce NaN cosine distance → NaN score. The NaN guard
	// must break out of the loop and return false (not a duplicate).
	h := &Handler{
		postgres:  pg,
		embedder:  &livepgEmbedder{},
		logger:    logger,
	}
	newEmbed := deterministicUnitVecHandlers("fresh text that is definitely not a duplicate", livepgEmbedDim)
	got := h.isDuplicate(ctx, newEmbed, cubeID, "", "")
	if got {
		t.Fatal("isDuplicate returned true for a NaN-scored row — NaN guard failed, the insert would be silently dropped")
	}
	t.Logf("NaN guard verified: isDuplicate correctly returned false when DB has a zero-vector memory")
}

// vecToPgVector formats a []float32 as a pgvector string literal "[v1,v2,...]".
func vecToPgVector(v []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%g", x)
	}
	sb.WriteByte(']')
	return sb.String()
}
