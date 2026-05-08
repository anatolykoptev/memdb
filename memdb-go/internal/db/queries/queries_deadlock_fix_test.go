package queries_test

// queries_deadlock_fix_test.go — SQL shape tests for deadlock-prevention changes.
//
// Verifies that DeleteByPropertyIDs and IncrRetrievalCount use a WITH locked AS
// CTE with ORDER BY ... FOR UPDATE before the data-changing operation. This
// ordering guarantees that concurrent DELETE and UPDATE acquire row locks in
// the same deterministic sequence, preventing the SQLSTATE 40P01 deadlock
// documented in the MemDB bug log.
//
// No Postgres connection required — tests run against the SQL string constants.

import (
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// TestDeleteByPropertyIDs_DeterministicLockOrder asserts that the DELETE query
// acquires row locks via a CTE that orders by property id before deleting,
// so the lock-acquisition sequence is identical to IncrRetrievalCount.
func TestDeleteByPropertyIDs_DeterministicLockOrder(t *testing.T) {
	sql := queries.DeleteByPropertyIDs

	for _, want := range []string{
		"WITH locked AS",
		"ORDER BY",
		"FOR UPDATE",
		"DELETE FROM",
		"USING locked",
		"WHERE m.id = l.id",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("DeleteByPropertyIDs SQL missing fragment %q\nGot:\n%s", want, sql)
		}
	}
}

// TestIncrRetrievalCount_DeterministicLockOrder asserts that the UPDATE query
// acquires row locks via a CTE that orders by property id before updating,
// so the lock-acquisition sequence is identical to DeleteByPropertyIDs.
func TestIncrRetrievalCount_DeterministicLockOrder(t *testing.T) {
	sql := queries.IncrRetrievalCount

	for _, want := range []string{
		"WITH locked AS",
		"ORDER BY",
		"FOR UPDATE",
		"UPDATE",
		"FROM locked",
		"WHERE m.id = l.id",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("IncrRetrievalCount SQL missing fragment %q\nGot:\n%s", want, sql)
		}
	}
}
