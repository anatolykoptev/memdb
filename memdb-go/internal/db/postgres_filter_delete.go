package db

// postgres_filter_delete.go — filter-driven DELETE for the Memory table.
//
// Mirrors Python's delete_node_by_prams in graph_dbs/polardb/maintenance.py:
// combines user_name (writable_cube_ids) conditions OR-joined with the
// pre-built filter conditions from internal/filter.BuildAGEWhereConditions.
// Values are inlined into the SQL string — AGE-compatible heredocs do not
// support $1 placeholders. The filter package is the sole input-sanitation
// boundary, so callers MUST pass only strings emitted by BuildAGEWhereConditions.
// cubeIDs are additionally validated here against cubeIDRe to prevent
// injection via request bodies.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/anatolykoptev/memdb/memdb-go/internal/filter"
)

// cubeIDRe enforces a strict alphabet for cube/user identifiers.
// Matches values accepted by the MemOS REST layer (letters, digits,
// underscore, hyphen; up to 64 chars). Inlined into AGE WHERE clauses.
var cubeIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// validateCubeIDs rejects any identifier that does not match cubeIDRe.
// Returns a descriptive error naming the first offending value.
func validateCubeIDs(cubeIDs []string) error {
	for _, id := range cubeIDs {
		if !cubeIDRe.MatchString(id) {
			return fmt.Errorf("invalid cube_id %q: must match %s", id, cubeIDRe.String())
		}
	}
	return nil
}

// buildUserNameConditions returns a single parenthesised OR-joined clause that
// matches any of the provided cubeIDs against properties->>'user_name'.
// Returns "" when cubeIDs is empty. Mirrors Python's user_name_conditions.
func buildUserNameConditions(cubeIDs []string) string {
	if len(cubeIDs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cubeIDs))
	for _, id := range cubeIDs {
		parts = append(parts, fmt.Sprintf(
			`ag_catalog.agtype_access_operator(properties::text::agtype, '"user_name"'::agtype) = '"%s"'::agtype`,
			id,
		))
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// DeleteByFilter deletes memory nodes matching the given filter conditions,
// scoped by cubeIDs (user_name). Returns (deletedCount, deletedPropertyIDs, error).
//
// The deleted property IDs are pre-queried inside the same transaction so
// callers can perform VSET eviction, Qdrant cleanup and cache invalidation
// without a second round-trip. The SELECT and DELETE run against the same
// snapshot, so the returned IDs always correspond to the actually-deleted rows.
//
// At least one cubeID is required — a filter delete without user scoping would
// span all users and is rejected as unsafe.
func (p *Postgres) DeleteByFilter(
	ctx context.Context,
	cubeIDs []string,
	filterConditions []string,
) (int64, []string, error) {
	if len(cubeIDs) == 0 {
		return 0, nil, errors.New("DeleteByFilter: at least one cube_id is required")
	}
	if err := validateCubeIDs(cubeIDs); err != nil {
		return 0, nil, err
	}
	if len(filterConditions) == 0 {
		return 0, nil, errors.New("DeleteByFilter: no filter conditions provided")
	}

	userNameClause := buildUserNameConditions(cubeIDs)
	clauses := make([]string, 0, len(filterConditions)+1)
	clauses = append(clauses, filterConditions...)
	clauses = append(clauses, userNameClause)
	whereSQL := strings.Join(clauses, " AND ")

	selectSQL := fmt.Sprintf(
		`SELECT ag_catalog.agtype_access_operator(properties::text::agtype, '"id"'::agtype)::text FROM %s."Memory" WHERE %s`,
		graphName, whereSQL,
	)
	deleteSQL := fmt.Sprintf(
		`DELETE FROM %s."Memory" WHERE %s`,
		graphName, whereSQL,
	)

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("DeleteByFilter: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ids, err := collectDeletedIDs(ctx, tx, selectSQL)
	if err != nil {
		return 0, nil, fmt.Errorf("DeleteByFilter: pre-query ids: %w", err)
	}

	tag, err := tx.Exec(ctx, deleteSQL)
	if err != nil {
		return 0, nil, fmt.Errorf("DeleteByFilter: exec delete: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("DeleteByFilter: commit: %w", err)
	}
	return tag.RowsAffected(), ids, nil
}

// collectDeletedIDs runs the pre-query SELECT inside the active transaction
// and unwraps each returned agtype string ("\"<uuid>\"") to a plain UUID.
func collectDeletedIDs(ctx context.Context, tx pgx.Tx, selectSQL string) ([]string, error) {
	rows, err := tx.Query(ctx, selectSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		// agtype text is the JSON form — strip wrapping quotes if present.
		if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
			raw = raw[1 : len(raw)-1]
		}
		if raw != "" {
			ids = append(ids, raw)
		}
	}
	return ids, rows.Err()
}

// DeleteByFileIDs is a thin wrapper around DeleteByFilter that constructs a
// filter matching memory nodes whose file_ids array contains any of the given
// IDs. The wrapper exists to keep the happy path in NativeDelete short and to
// share one code path for all filter-based deletes.
func (p *Postgres) DeleteByFileIDs(
	ctx context.Context,
	cubeIDs []string,
	fileIDs []string,
) (int64, []string, error) {
	if len(fileIDs) == 0 {
		return 0, nil, errors.New("DeleteByFileIDs: fileIDs is empty")
	}
	items := make([]any, 0, len(fileIDs))
	for _, id := range fileIDs {
		items = append(items, id)
	}
	raw := map[string]any{
		"file_ids": map[string]any{string(filter.OpIn): items},
	}
	f, err := filter.Parse(raw)
	if err != nil {
		return 0, nil, fmt.Errorf("DeleteByFileIDs: build filter: %w", err)
	}
	conditions, err := filter.BuildAGEWhereConditions(f)
	if err != nil {
		return 0, nil, fmt.Errorf("DeleteByFileIDs: render filter: %w", err)
	}
	return p.DeleteByFilter(ctx, cubeIDs, conditions)
}
