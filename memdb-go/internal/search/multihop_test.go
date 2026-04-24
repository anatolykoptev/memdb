package search

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// mockExpansionPG satisfies postgresClient for the expansion call only.
// All other methods no-op and are never exercised by expandViaGraph.
type mockExpansionPG struct {
	mockPostgres
	returnExpansions []db.GraphExpansion
	returnErr        error
	gotSeedIDs       []string
	gotDepth         int
	gotLimit         int
	gotCubeID        string
	gotPersonID      string
	callCount        int
}

func (m *mockExpansionPG) MultiHopEdgeExpansion(_ context.Context, seedIDs []string, cubeID, personID string, depth, limit int, _ string) ([]db.GraphExpansion, error) {
	m.callCount++
	m.gotSeedIDs = append([]string(nil), seedIDs...)
	m.gotCubeID = cubeID
	m.gotPersonID = personID
	m.gotDepth = depth
	m.gotLimit = limit
	if m.returnErr != nil {
		return nil, m.returnErr
	}
	return m.returnExpansions, nil
}

func mkSeeds(n int) []MergedResult {
	out := make([]MergedResult, n)
	for i := 0; i < n; i++ {
		out[i] = MergedResult{
			ID:    rune2id(i),
			Score: 1.0 - float64(i)*0.01,
		}
	}
	return out
}

// rune2id produces stable deterministic IDs "seed-0", "seed-1", ...
func rune2id(i int) string {
	return "seed-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestExpandViaGraph_Disabled_IsNoOp(t *testing.T) {
	// Default env state: MEMDB_SEARCH_MULTIHOP is unset → disabled.
	// Expansion must return input unchanged and never call the DB.
	pg := &mockExpansionPG{
		returnExpansions: []db.GraphExpansion{{ID: "x", Hop: 1, SeedID: "seed-0"}},
	}
	seeds := mkSeeds(3)
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), seeds, nil, "cube", "user", "")
	if pg.callCount != 0 {
		t.Fatalf("DB called while disabled: callCount=%d", pg.callCount)
	}
	if len(got) != len(seeds) {
		t.Fatalf("len(got)=%d want %d", len(got), len(seeds))
	}
	for i := range seeds {
		if got[i].ID != seeds[i].ID {
			t.Fatalf("seed %d mutated: got %q want %q", i, got[i].ID, seeds[i].ID)
		}
	}
}

func TestExpandViaGraph_EmptyInput(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	pg := &mockExpansionPG{}
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), nil, nil, "cube", "user", "")
	if pg.callCount != 0 {
		t.Fatalf("DB called for empty input: callCount=%d", pg.callCount)
	}
	if len(got) != 0 {
		t.Fatalf("len(got)=%d want 0", len(got))
	}
}

func TestExpandViaGraph_NilPostgres(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	seeds := mkSeeds(2)
	got := expandViaGraph(context.Background(), nil, discardTestLogger(), seeds, nil, "cube", "user", "")
	if len(got) != len(seeds) {
		t.Fatalf("len(got)=%d want %d", len(got), len(seeds))
	}
}

func TestExpandViaGraph_DBError_FallsBack(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	pg := &mockExpansionPG{returnErr: errors.New("boom")}
	seeds := mkSeeds(2)
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), seeds, nil, "cube", "user", "")
	if pg.callCount != 1 {
		t.Fatalf("expected 1 DB call, got %d", pg.callCount)
	}
	if len(got) != len(seeds) {
		t.Fatalf("expected fallback to original on error: len(got)=%d want %d", len(got), len(seeds))
	}
}

func TestExpandViaGraph_HopDecay(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	// Seed seed-0 has score 1.0. One expansion at hop 1, one at hop 2.
	pg := &mockExpansionPG{
		returnExpansions: []db.GraphExpansion{
			{ID: "n1", Hop: 1, SeedID: "seed-0", Properties: `{"id":"n1"}`},
			{ID: "n2", Hop: 2, SeedID: "seed-0", Properties: `{"id":"n2"}`},
		},
	}
	seeds := []MergedResult{{ID: "seed-0", Score: 1.0}}
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), seeds, nil, "cube", "user", "")

	// With origSize=1 and cap=2, we expect [seed-0, one of n1/n2]. But
	// the higher-scoring hop-1 neighbor (0.8) wins over hop-2 (0.64).
	if len(got) != 2 {
		t.Fatalf("expected 2 results capped at 2x, got %d: %+v", len(got), got)
	}
	var n1, n2 *MergedResult
	for i, r := range got {
		switch r.ID {
		case "n1":
			n1 = &got[i]
		case "n2":
			n2 = &got[i]
		}
	}
	if n1 == nil {
		t.Fatalf("n1 missing from result; cap evicted higher-scoring neighbor: %+v", got)
	}
	const eps = 1e-9
	if math.Abs(n1.Score-0.8) > eps {
		t.Fatalf("n1 hop-1 score: got %v want 0.8", n1.Score)
	}
	if n2 != nil && math.Abs(n2.Score-0.64) > eps {
		t.Fatalf("n2 hop-2 score: got %v want 0.64", n2.Score)
	}
}

func TestExpandViaGraph_CappedAt2x(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	// 10 seeds → cap at 20. Add 100 expansions at hop 1 — must be trimmed to 10 (20 total).
	seeds := mkSeeds(10)
	expansions := make([]db.GraphExpansion, 100)
	for i := range expansions {
		expansions[i] = db.GraphExpansion{
			ID:     "expanded-" + itoa(i),
			Hop:    1,
			SeedID: "seed-0",
		}
	}
	pg := &mockExpansionPG{returnExpansions: expansions}
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), seeds, nil, "cube", "user", "")

	if len(got) != 20 {
		t.Fatalf("expected cap=20, got %d", len(got))
	}
	// DB limit should also be the cap.
	if pg.gotLimit != 20 {
		t.Fatalf("DB limit arg: got %d want 20", pg.gotLimit)
	}
	// Passed seed IDs.
	if len(pg.gotSeedIDs) != 10 {
		t.Fatalf("seed IDs: got %d want 10", len(pg.gotSeedIDs))
	}
	if pg.gotDepth != defaultMultihopMaxDepth {
		t.Fatalf("depth: got %d want %d", pg.gotDepth, defaultMultihopMaxDepth)
	}
	if pg.gotCubeID != "cube" || pg.gotPersonID != "user" {
		t.Fatalf("scope args: cube=%q user=%q", pg.gotCubeID, pg.gotPersonID)
	}
}

func TestExpandViaGraph_DuplicateOfSeed_Ignored(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	seeds := []MergedResult{{ID: "seed-0", Score: 1.0}}
	// DB returns a neighbor that happens to equal a seed (shouldn't happen
	// given the CTE filter, but guard anyway).
	pg := &mockExpansionPG{returnExpansions: []db.GraphExpansion{
		{ID: "seed-0", Hop: 1, SeedID: "seed-0"},
	}}
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), seeds, nil, "cube", "user", "")
	if len(got) != 1 {
		t.Fatalf("duplicate of seed merged: len=%d", len(got))
	}
	if got[0].Score != 1.0 {
		t.Fatalf("seed score overwritten by expansion: %v", got[0].Score)
	}
}

func TestExpandViaGraph_OrphanSeedIDSkipped(t *testing.T) {
	t.Setenv("MEMDB_SEARCH_MULTIHOP", "true")
	seeds := []MergedResult{{ID: "seed-0", Score: 1.0}}
	// Expansion claims to come from "seed-99" which we never submitted.
	// Expected to be dropped defensively.
	pg := &mockExpansionPG{returnExpansions: []db.GraphExpansion{
		{ID: "n1", Hop: 1, SeedID: "seed-99"},
	}}
	got := expandViaGraph(context.Background(), pg, discardTestLogger(), seeds, nil, "cube", "user", "")
	if len(got) != 1 {
		t.Fatalf("orphan-seed expansion leaked: len=%d %+v", len(got), got)
	}
}
