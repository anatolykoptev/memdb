package db

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// TestVectorSearchMultiCube_SQLShape verifies the SQL constant has the
// cross-cube filter (user_name = ANY($2::text[])) and retains the same
// invariants as the single-cube VectorSearch (halfvec index, status filter).
func TestVectorSearchMultiCube_SQLShape(t *testing.T) {
	cases := []struct {
		name   string
		needle string
	}{
		{"uses ANY for user_name", "properties->>'user_name' = ANY($2::text[])"},
		{"filters activated status", "properties->>'status' = 'activated'"},
		{"uses halfvec index", "embedding::halfvec(1024) <=> $1::halfvec(1024)"},
		{"filters by memory_type array", "properties->>'memory_type' = ANY($3)"},
		{"optional agent_id filter", "$5::text = ''"},
		{"requires non-null embedding", "embedding IS NOT NULL"},
		{"limit param", "LIMIT $4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(queries.VectorSearchMultiCube, c.needle) { //nolint:gocritic // haystack/needle order is correct
				t.Errorf("VectorSearchMultiCube missing %q", c.needle)
			}
		})
	}
}

// TestVectorSearchMultiCube_DiffersFromSingleCube verifies the multi-cube
// query does NOT use the single-cube equality filter — it must use ANY.
func TestVectorSearchMultiCube_DiffersFromSingleCube(t *testing.T) {
	const singleCubeFilter = "properties->>'user_name' = $2\n"
	if strings.Contains(queries.VectorSearchMultiCube, singleCubeFilter) {
		t.Error("VectorSearchMultiCube should not use single-cube equality filter; want ANY")
	}
	// Sanity: single-cube query still uses the simple equality.
	if !strings.Contains(queries.VectorSearch, singleCubeFilter) {
		t.Error("VectorSearch single-cube variant must still use equality filter")
	}
}
