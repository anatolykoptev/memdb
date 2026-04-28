package db

// postgres_temporal.go — M11 F7: temporal-index helpers.
//
// Atomic facts (F8) write an `event_dates` JSON array on properties. F7 indexes
// that field (migration 0024_event_dates.sql) and queries it here to support
// "boost results matching the query's date range" semantics in the search
// pipeline (internal/search/temporal_augment.go).
//
// All callers are read-only — no writes to event_dates live in this file.

import (
	"context"
	"fmt"
)

// TemporalMatch is one row returned by SearchMemoriesByDateRange. Mirrors the
// minimal shape needed by the search-stage booster: ID identifies the memory
// and is matched against MergedResult.ID; Properties carries the JSON blob in
// case a future caller wants to inspect attributed_to / linked_memory_ids.
type TemporalMatch struct {
	ID         string // properties->>'id' (UUID string)
	Properties string // raw JSON properties
}

// SearchMemoriesByDateRange returns activated memory rows scoped to (cubeID)
// whose `event_dates` array contains at least one ISO date in [start, end]
// (inclusive). Both start and end MUST be "YYYY-MM-DD" strings — caller is
// responsible for sanitising; no parsing is done here. Either may be empty
// to mean "open-ended" on that side (e.g. start="2024-01-01" end="" → all
// dates >= 2024-01-01).
//
// Implementation: unnests the JSON array via jsonb_array_elements_text and
// filters by lexical range — safe because YYYY-MM-DD is sortable as text.
//
// The GIN index from migration 0024 helps the planner skip the WHERE clause
// for rows that never carry event_dates; the unnest is the bottleneck on
// matching rows, so limit is enforced in SQL.
func (p *Postgres) SearchMemoriesByDateRange(
	ctx context.Context,
	cubeID, start, end string,
	limit int,
) ([]TemporalMatch, error) {
	if cubeID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	// Build the date predicate. We always emit something so the prepared
	// statement is stable; empty bounds become NULL and the comparison is
	// elided via COALESCE.
	const q = `
SELECT properties->>(('id'::text)) AS prop_id,
       properties::text             AS props
FROM memos_graph."Memory"
WHERE properties->>(('user_name'::text)) = $1
  AND properties ? 'event_dates'
  AND EXISTS (
      SELECT 1
      FROM jsonb_array_elements_text(properties->'event_dates') AS d(val)
      WHERE ($2 = '' OR d.val >= $2)
        AND ($3 = '' OR d.val <= $3)
  )
LIMIT $4`
	rows, err := p.pool.Query(ctx, q, cubeID, start, end, limit)
	if err != nil {
		return nil, fmt.Errorf("temporal date range search (%s, [%s, %s]): %w", cubeID, start, end, err)
	}
	defer rows.Close()
	out := make([]TemporalMatch, 0, 16)
	for rows.Next() {
		var m TemporalMatch
		if err := rows.Scan(&m.ID, &m.Properties); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
