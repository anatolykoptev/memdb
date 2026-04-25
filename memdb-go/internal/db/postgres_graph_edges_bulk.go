package db

// postgres_graph_edges_bulk.go — bulk-insert + session-neighbor queries used by
// the M8 Stream 10 structural-edge emitter (handlers/add_structural_edges.go).
//
// Kept separate from postgres_graph_edges.go so the original D3-era file stays
// focused on per-edge writes and bi-temporal invalidation, and so future
// structural-edge work doesn't push that file past the 200-line target.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// MemoryEdgeRow is one row for BulkInsertMemoryEdges. Fields map 1:1 to the
// memory_edges columns the bulk INSERT writes (created_at is shared per call).
type MemoryEdgeRow struct {
	FromID     string
	ToID       string
	Relation   string
	Confidence float64
	Rationale  string
}

// SessionMemoryNeighbor is one activated LTM/UserMemory row in the same
// (cube, session). Returned by GetSessionMemoryNeighbors and consumed by the
// SAME_SESSION / TIMELINE_NEXT / SIMILAR_COSINE_HIGH emitters.
type SessionMemoryNeighbor struct {
	ID            string
	CreatedAt     string
	EmbeddingStr  string    // raw pgvector text "[...]" — empty if NULL
	Embedding     []float32 // parsed lazily by callers that need it
}

// BulkInsertMemoryEdges performs a single multi-row INSERT of structural
// edges. Empty input is a no-op. Confidence is clamped to [0, 1] per row.
// Idempotent via ON CONFLICT — replays of the same /add request do not
// double-write.
//
// All edges in one call share createdAt (the ingest timestamp). Caller must
// pre-filter self-edges (from == to) and empty IDs — we just skip them here
// rather than erroring, because the structural emitter computes large
// candidate sets and a defensive filter at the boundary is cheaper than
// validating every helper.
func (p *Postgres) BulkInsertMemoryEdges(ctx context.Context, rows []MemoryEdgeRow, createdAt string) error {
	if len(rows) == 0 {
		return nil
	}
	fromIDs := make([]string, 0, len(rows))
	toIDs := make([]string, 0, len(rows))
	relations := make([]string, 0, len(rows))
	confidences := make([]float64, 0, len(rows))
	rationales := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.FromID == "" || r.ToID == "" || r.Relation == "" || r.FromID == r.ToID {
			continue
		}
		c := r.Confidence
		if c < 0 {
			c = 0
		} else if c > 1 {
			c = 1
		}
		fromIDs = append(fromIDs, r.FromID)
		toIDs = append(toIDs, r.ToID)
		relations = append(relations, r.Relation)
		confidences = append(confidences, c)
		rationales = append(rationales, r.Rationale)
	}
	if len(fromIDs) == 0 {
		return nil
	}
	_, err := p.pool.Exec(ctx, queries.BulkInsertMemoryEdges,
		fromIDs, toIDs, relations, createdAt, confidences, rationales)
	if err != nil {
		return fmt.Errorf("bulk insert %d memory edges: %w", len(fromIDs), err)
	}
	return nil
}

// GetSessionMemoryNeighbors returns up to limit activated LTM/UserMemory rows
// for (cubeID, sessionID), sorted by created_at ASC. Empty sessionID short-
// circuits to nil — the caller (structural-edge emitter) skips edge work
// entirely when no session is set, so we don't waste a query.
//
// Embedding text is returned but NOT parsed here — most callers only need
// SAME_SESSION/TIMELINE_NEXT, which are embedding-free. The SIMILAR_COSINE
// path parses on demand to keep the no-similar-edges hot path allocation-free.
func (p *Postgres) GetSessionMemoryNeighbors(ctx context.Context, cubeID, sessionID string, limit int) ([]SessionMemoryNeighbor, error) {
	if cubeID == "" || sessionID == "" {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.SessionMemoryNeighbors, graphName)
	rows, err := p.pool.Query(ctx, q, cubeID, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("session memory neighbors: %w", err)
	}
	defer rows.Close()

	var out []SessionMemoryNeighbor
	for rows.Next() {
		var n SessionMemoryNeighbor
		if err := rows.Scan(&n.ID, &n.CreatedAt, &n.EmbeddingStr); err != nil {
			return nil, fmt.Errorf("session neighbors scan: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
