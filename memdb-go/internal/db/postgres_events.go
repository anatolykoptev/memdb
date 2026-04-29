package db

// postgres_events.go — user_events table operations (M11 F3).
//
// Mirrors postgres_profiles.go in shape: typed Insert + bounded fetchers.
// Three search modes back the F3 inject stage:
//   - SearchEventsByDate    : event_date in [start, end]   — direct btree lookup
//   - SearchEventsByTag     : tags && $tags                — GIN tag-overlap
//   - SearchEventsByCosine  : embedding <=> $vec ORDER BY  — HNSW halfvec index
//
// All three are scoped by (cube_id, user_id) so cross-tenant leakage is
// impossible regardless of which search path the F3 stage picks.
//
// Embedding is passed as the pgvector text-literal form (FormatVector) — same
// pattern as postgres_search_vector.go, no pgvector-go dependency.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
)

// EventEntry is one row from memos_graph.user_events.
type EventEntry struct {
	ID        uuid.UUID
	CubeID    string
	UserID    string
	EventText string
	EventDate *time.Time
	Tags      []string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// InsertEventParams is the input for InsertEvent.
type InsertEventParams struct {
	CubeID    string
	UserID    string
	EventText string
	EventDate *time.Time
	Tags      []string
	Embedding []float32 // optional; nil leaves the column NULL
}

// ErrEventInvalidKey is returned when required identifiers are missing.
var ErrEventInvalidKey = errors.New("user event: cube_id, user_id and event_text are required")

const eventReadColumns = `id, cube_id, user_id, event_text, event_date, tags, created_at, updated_at`

// InsertEvent inserts a single row and returns its assigned id + timestamps.
// The embedding column accepts NULL, so callers can write rows even when the
// embedder is unavailable (search will simply skip them on the cosine path).
func (p *Postgres) InsertEvent(ctx context.Context, params InsertEventParams) (EventEntry, error) {
	if params.CubeID == "" || params.UserID == "" || params.EventText == "" {
		return EventEntry{}, fmt.Errorf("InsertEvent: %w", ErrEventInvalidKey)
	}

	tags := params.Tags
	if tags == nil {
		tags = []string{}
	}

	// Embedding column is bound as text; the SQL casts to ::vector(1024).
	// Passing the empty string with a NULLIF lets us write NULL when no
	// embedding was produced.
	var embStr string
	if len(params.Embedding) > 0 {
		embStr = FormatVector(params.Embedding)
	}

	const q = `
INSERT INTO memos_graph.user_events
    (cube_id, user_id, event_text, event_date, tags, embedding)
VALUES
    ($1, $2, $3, $4, $5, NULLIF($6, '')::vector(1024))
RETURNING id, cube_id, user_id, event_text, event_date, tags, created_at, updated_at`

	row := p.pool.QueryRow(ctx, q,
		params.CubeID, params.UserID, params.EventText, params.EventDate, tags, embStr,
	)
	var out EventEntry
	if err := row.Scan(
		&out.ID, &out.CubeID, &out.UserID, &out.EventText,
		&out.EventDate, &out.Tags, &out.CreatedAt, &out.UpdatedAt,
	); err != nil {
		return EventEntry{}, fmt.Errorf("InsertEvent scan: %w", err)
	}
	return out, nil
}

// SearchEventsByDate returns events whose event_date falls in [start, end]
// for the given (cube_id, user_id), ordered by date DESC. Inclusive bounds.
// limit ≤ 0 falls back to the package default (8).
func (p *Postgres) SearchEventsByDate(
	ctx context.Context, cubeID, userID string, start, end time.Time, limit int,
) ([]EventEntry, error) {
	if cubeID == "" || userID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 8
	}
	const q = `
SELECT ` + eventReadColumns + `
FROM memos_graph.user_events
WHERE cube_id = $1
  AND user_id = $2
  AND event_date IS NOT NULL
  AND event_date BETWEEN $3 AND $4
ORDER BY event_date DESC
LIMIT $5`
	out, err := observability.MeasureQuery(ctx, "SearchEventsByDate", p.pool, func(conn *pgxpool.Conn) ([]EventEntry, error) {
		rows, qerr := conn.Query(ctx, q, cubeID, userID, start, end, limit)
		if qerr != nil {
			return nil, qerr
		}
		defer rows.Close()
		results, scanErr := scanEventRows(rows)
		observability.AddRowsScanned(ctx, "SearchEventsByDate", int64(len(results)))
		return results, scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("SearchEventsByDate query: %w", err)
	}
	return out, nil
}

// SearchEventsByTag returns events whose `tags` array overlaps the given
// `tags` slice (Postgres `&&` operator, GIN-accelerated). Tag values are
// case-sensitive — callers normalise to lowercase upstream.
func (p *Postgres) SearchEventsByTag(
	ctx context.Context, cubeID, userID string, tags []string, limit int,
) ([]EventEntry, error) {
	if cubeID == "" || userID == "" || len(tags) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 8
	}
	const q = `
SELECT ` + eventReadColumns + `
FROM memos_graph.user_events
WHERE cube_id = $1
  AND user_id = $2
  AND tags && $3
ORDER BY created_at DESC
LIMIT $4`
	rows, err := p.pool.Query(ctx, q, cubeID, userID, tags, limit)
	if err != nil {
		return nil, fmt.Errorf("SearchEventsByTag query: %w", err)
	}
	defer rows.Close()
	return scanEventRows(rows)
}

// SearchEventsByCosine returns the top-k events ranked by cosine similarity
// against `query`. Skips rows where embedding IS NULL (cannot rank). Mirrors
// the query shape used by Memory.embedding HNSW lookups.
func (p *Postgres) SearchEventsByCosine(
	ctx context.Context, cubeID, userID string, query []float32, k int,
) ([]EventEntry, error) {
	if cubeID == "" || userID == "" || len(query) == 0 {
		return nil, nil
	}
	if k <= 0 {
		k = 8
	}
	vecStr := FormatVector(query)
	const q = `
SELECT ` + eventReadColumns + `
FROM memos_graph.user_events
WHERE cube_id = $1
  AND user_id = $2
  AND embedding IS NOT NULL
ORDER BY embedding::halfvec(1024) <=> $3::halfvec(1024)
LIMIT $4`
	rows, err := p.pool.Query(ctx, q, cubeID, userID, vecStr, k)
	if err != nil {
		return nil, fmt.Errorf("SearchEventsByCosine query: %w", err)
	}
	defer rows.Close()
	return scanEventRows(rows)
}

// scanEventRows is the common scan loop shared by all three search modes.
// Embedding is NOT selected (search results stay small).
func scanEventRows(rows pgx.Rows) ([]EventEntry, error) {
	var out []EventEntry
	for rows.Next() {
		var e EventEntry
		if err := rows.Scan(
			&e.ID, &e.CubeID, &e.UserID, &e.EventText,
			&e.EventDate, &e.Tags, &e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan event row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event rows: %w", err)
	}
	return out, nil
}
