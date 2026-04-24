package queries_test

// queries_memory_ltm_test.go — SQL string smoke tests for HNSW near-duplicate queries.
// No Postgres connection required.

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

func TestFindNearDuplicatesHNSWSQL_Shape(t *testing.T) {
	sql := queries.FindNearDuplicatesHNSWSQL()
	for _, want := range []string{
		"CROSS JOIN LATERAL",
		"ORDER BY m.embedding <=> a.embedding",
		"LIMIT $4",
		"m.id > a.id",
		"1 - (a.embedding <=> b.embedding) >= $2",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("HNSW SQL missing fragment %q", want)
		}
	}
}

func TestFindNearDuplicatesHNSWByIDsSQL_Shape(t *testing.T) {
	sql := queries.FindNearDuplicatesHNSWByIDsSQL()
	for _, want := range []string{
		"CROSS JOIN LATERAL",
		"a.properties->>(('id'::text)) = ANY($2)",
		"LIMIT $4",
		"LIMIT $5",
		"1 - (a.embedding <=> b.embedding) >= $3",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("HNSW ByIDs SQL missing fragment %q", want)
		}
	}
}
