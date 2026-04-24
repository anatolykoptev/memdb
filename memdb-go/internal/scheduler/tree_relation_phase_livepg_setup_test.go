//go:build livepg

package scheduler

// tree_relation_phase_livepg_setup_test.go — DB setup + cleanup for the
// live-Postgres D3 relation-phase integration test. Split out of the helpers
// file so each _test.go stays ≤200 lines per repo policy.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// buildLivepgInsertNodes produces db.MemoryInsertNode entries for every raw
// memory, stamped with cubeID as user_name so cleanup can reach them by
// predicate without touching any prod rows. Each row drives the
// InsertMemoryNode path → both properties->>'id' and the pgvector embedding
// column are filled.
func buildLivepgInsertNodes(cubeID string, rawMems []livepgRawMemory) ([]db.MemoryInsertNode, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	nodes := make([]db.MemoryInsertNode, 0, len(rawMems))
	for _, m := range rawMems {
		props := map[string]any{
			"id":               m.ID,
			"memory":           m.Text,
			"memory_type":      "LongTermMemory",
			"user_name":        cubeID,
			"user_id":          cubeID,
			"status":           "activated",
			"created_at":       now,
			"updated_at":       now,
			"confidence":       0.9,
			"hierarchy_level":  "raw",
			"parent_memory_id": nil,
			"source":           "livepg-test",
			"tags":             []string{"livepg-test"},
		}
		raw, err := json.Marshal(props)
		if err != nil {
			return nil, fmt.Errorf("marshal props: %w", err)
		}
		nodes = append(nodes, db.MemoryInsertNode{
			ID:             m.ID,
			PropertiesJSON: raw,
			EmbeddingVec:   db.FormatVector(m.Embedding),
		})
	}
	return nodes, nil
}

// openLivePG reads MEMDB_LIVE_PG_DSN and constructs a *db.Postgres via the
// same NewPostgres used by cmd/mcp-server/main.go, so we exercise the real
// migrations + AGE bootstrap path. Callers t.Skip when the DSN is empty.
func openLivePG(ctx context.Context, t *testing.T, logger *slog.Logger) *db.Postgres {
	t.Helper()
	dsn := os.Getenv("MEMDB_LIVE_PG_DSN")
	if dsn == "" {
		t.Skip("MEMDB_LIVE_PG_DSN not set; skipping live-Postgres relation-phase test")
	}
	pg, err := db.NewPostgres(ctx, dsn, logger)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	return pg
}

// cleanupLivepgCube deletes every row the test wrote for cubeID:
//   - memory_edges whose from_id OR to_id points at any Memory row owned by cubeID,
//   - tree_consolidation_log rows for cubeID,
//   - Memory rows owned by cubeID,
//   - cubes row (only if it exists — InsertMemoryNodes does NOT create one, but
//     we defend in case of future auto-upsert behaviour).
//
// Each DELETE is isolated with its own Exec so a failure in one doesn't
// prevent the others — test failure must never leave the cube half-live.
func cleanupLivepgCube(ctx context.Context, t *testing.T, pg *db.Postgres, cubeID string) {
	t.Helper()
	pool := pg.Pool()
	if pool == nil {
		return
	}

	const delEdgesSQL = `
DELETE FROM memos_graph.memory_edges
WHERE from_id IN (
	SELECT properties->>('id'::text)
	FROM memos_graph."Memory"
	WHERE properties->>('user_name'::text) = $1
)
   OR to_id IN (
	SELECT properties->>('id'::text)
	FROM memos_graph."Memory"
	WHERE properties->>('user_name'::text) = $1
)`
	const delTreeLogSQL = `DELETE FROM memos_graph.tree_consolidation_log WHERE cube_id = $1`
	const delMemorySQL = `
DELETE FROM memos_graph."Memory"
WHERE properties->>('user_name'::text) = $1`
	const delCubesSQL = `DELETE FROM memos_graph.cubes WHERE cube_id = $1`

	for _, step := range []struct {
		label string
		sql   string
	}{
		{"memory_edges", delEdgesSQL},
		{"tree_consolidation_log", delTreeLogSQL},
		{"Memory", delMemorySQL},
		{"cubes", delCubesSQL},
	} {
		if _, err := pool.Exec(ctx, step.sql, cubeID); err != nil {
			t.Logf("livepg cleanup %s (cube=%s): %v", step.label, cubeID, err)
		}
	}
}
