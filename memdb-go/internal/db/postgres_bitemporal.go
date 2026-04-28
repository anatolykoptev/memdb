package db

// postgres_bitemporal.go — M11 F11 bi-temporal edge invalidation.
//
// Read + bulk-invalidate helpers used by the edge invalidation judge
// (internal/llm/edge_extractor.go) and the periodic bitemporal_validator
// loop (internal/scheduler/bitemporal_validator.go).
//
// Naming note: the schema columns are valid_at / invalid_at (TEXT), set up
// by 0005_memory_edges.sql and 0007_entity_edges.sql. The F11 plan refers
// to "invalidated_at" — same concept, the existing column name wins to
// avoid touching every existing query that already filters on invalid_at.

import (
	"context"
	"fmt"
)

// BiTemporalEdgeRef is one row of an existing edge that the F11 judge may
// flag for invalidation. Predicate is the relation/predicate string (memory
// edges call it "relation", entity edges call it "predicate" — same idea).
type BiTemporalEdgeRef struct {
	// FromID is the subject identifier (memory_edges.from_id or
	// entity_edges.from_entity_id depending on which lookup ran).
	FromID string
	// ToID is the object identifier (memory_edges.to_id or
	// entity_edges.to_entity_id).
	ToID string
	// Predicate is memory_edges.relation or entity_edges.predicate.
	Predicate string
	// CreatedAt is the original ISO timestamp (TEXT in the column).
	CreatedAt string
}

// FetchActiveMemoryEdgesBySubject returns currently-valid memory_edges where
// from_id = subjectID and relation = predicate. Caller wraps results in
// FactRef and passes to the LLM judge.
//
// Bounded by limit; ordered by created_at DESC so the freshest competing
// facts come first when limit truncates.
func (p *Postgres) FetchActiveMemoryEdgesBySubject(
	ctx context.Context, subjectID, predicate string, limit int,
) ([]BiTemporalEdgeRef, error) {
	if subjectID == "" || predicate == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 8
	}
	const q = `
SELECT from_id, to_id, relation, COALESCE(created_at, '')
FROM memory_edges
WHERE from_id = $1
  AND relation = $2
  AND invalid_at IS NULL
ORDER BY created_at DESC NULLS LAST
LIMIT $3`
	rows, err := p.pool.Query(ctx, q, subjectID, predicate, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch active memory edges (%s,%s): %w", subjectID, predicate, err)
	}
	defer rows.Close()

	var out []BiTemporalEdgeRef
	for rows.Next() {
		var r BiTemporalEdgeRef
		if err := rows.Scan(&r.FromID, &r.ToID, &r.Predicate, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan memory edge row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory edges: %w", err)
	}
	return out, nil
}

// FetchActiveEntityEdgesBySubject returns currently-valid entity_edges where
// from_entity_id = subjectID and predicate = predicate, scoped to a user.
// Same bounded-DESC ordering as FetchActiveMemoryEdgesBySubject.
func (p *Postgres) FetchActiveEntityEdgesBySubject(
	ctx context.Context, userName, subjectID, predicate string, limit int,
) ([]BiTemporalEdgeRef, error) {
	if subjectID == "" || predicate == "" || userName == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 8
	}
	const q = `
SELECT from_entity_id, to_entity_id, predicate, COALESCE(created_at, '')
FROM entity_edges
WHERE user_name = $1
  AND from_entity_id = $2
  AND predicate = $3
  AND invalid_at IS NULL
ORDER BY created_at DESC NULLS LAST
LIMIT $4`
	rows, err := p.pool.Query(ctx, q, userName, subjectID, predicate, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch active entity edges (%s,%s,%s): %w", userName, subjectID, predicate, err)
	}
	defer rows.Close()

	var out []BiTemporalEdgeRef
	for rows.Next() {
		var r BiTemporalEdgeRef
		if err := rows.Scan(&r.FromID, &r.ToID, &r.Predicate, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan entity edge row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate entity edges: %w", err)
	}
	return out, nil
}

// InvalidateMemoryEdgesByTriples sets invalid_at on rows matching any of the
// provided (from_id, to_id, relation) triples. Returns the number of rows
// actually flipped (rows where invalid_at IS NULL becomes != NULL).
//
// Idempotent: re-running on already-invalidated rows is a no-op.
func (p *Postgres) InvalidateMemoryEdgesByTriples(
	ctx context.Context, triples []BiTemporalEdgeRef, invalidAt string,
) (int64, error) {
	if len(triples) == 0 || invalidAt == "" {
		return 0, nil
	}
	froms := make([]string, len(triples))
	tos := make([]string, len(triples))
	rels := make([]string, len(triples))
	for i, t := range triples {
		froms[i] = t.FromID
		tos[i] = t.ToID
		rels[i] = t.Predicate
	}
	const q = `
UPDATE memory_edges AS e
SET invalid_at = $4
FROM UNNEST($1::text[], $2::text[], $3::text[]) AS u(from_id, to_id, relation)
WHERE e.from_id = u.from_id
  AND e.to_id   = u.to_id
  AND e.relation = u.relation
  AND e.invalid_at IS NULL`
	tag, err := p.pool.Exec(ctx, q, froms, tos, rels, invalidAt)
	if err != nil {
		return 0, fmt.Errorf("invalidate memory edges by triples: %w", err)
	}
	return tag.RowsAffected(), nil
}

// InvalidateEntityEdgesByTriples is the entity_edges twin of
// InvalidateMemoryEdgesByTriples. Scoped to userName because entity_edges
// is partitioned per user.
func (p *Postgres) InvalidateEntityEdgesByTriples(
	ctx context.Context, userName string, triples []BiTemporalEdgeRef, invalidAt string,
) (int64, error) {
	if len(triples) == 0 || invalidAt == "" || userName == "" {
		return 0, nil
	}
	froms := make([]string, len(triples))
	tos := make([]string, len(triples))
	preds := make([]string, len(triples))
	for i, t := range triples {
		froms[i] = t.FromID
		tos[i] = t.ToID
		preds[i] = t.Predicate
	}
	const q = `
UPDATE entity_edges AS e
SET invalid_at = $5
FROM UNNEST($2::text[], $3::text[], $4::text[]) AS u(from_id, to_id, predicate)
WHERE e.user_name = $1
  AND e.from_entity_id = u.from_id
  AND e.to_entity_id   = u.to_id
  AND e.predicate      = u.predicate
  AND e.invalid_at IS NULL`
	tag, err := p.pool.Exec(ctx, q, userName, froms, tos, preds, invalidAt)
	if err != nil {
		return 0, fmt.Errorf("invalidate entity edges by triples: %w", err)
	}
	return tag.RowsAffected(), nil
}

// FetchFreshEntityEdgesForCube returns entity_edges this cube wrote at exactly
// `now` (the per-/add timestamp). These are the edges to feed into the F11
// invalidation judge — supersession decisions are scoped to the just-written
// batch so the LLM cost stays proportional to /add throughput rather than
// total graph size.
//
// limit caps the batch (callers pass edgeJudgeBatchCap from the handlers
// package). If a single /add exceeds the cap, the periodic
// bitemporal_validator loop will pick up the rest.
func (p *Postgres) FetchFreshEntityEdgesForCube(
	ctx context.Context, userName, now string, limit int,
) ([]BiTemporalEdgeRef, error) {
	if userName == "" || now == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 16
	}
	const q = `
SELECT from_entity_id, to_entity_id, predicate, COALESCE(created_at, '')
FROM entity_edges
WHERE user_name = $1
  AND created_at = $2
  AND invalid_at IS NULL
LIMIT $3`
	rows, err := p.pool.Query(ctx, q, userName, now, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch fresh entity edges: %w", err)
	}
	defer rows.Close()

	var out []BiTemporalEdgeRef
	for rows.Next() {
		var r BiTemporalEdgeRef
		if err := rows.Scan(&r.FromID, &r.ToID, &r.Predicate, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan fresh entity edge: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fresh entity edges: %w", err)
	}
	return out, nil
}

// StaleActiveEdgesQuery returns the SQL used by the bitemporal_validator loop
// to find edges that are still flagged valid but old enough to merit re-judging.
// Surfaced as a const + helper so the loop and tests share the same definition.
//
// Args: $1 = cutoff timestamp (ISO TEXT — older created_at means older edge),
//
//	$2 = user_name, $3 = limit
const StaleActiveEntityEdgesSQL = `
SELECT from_entity_id, to_entity_id, predicate, COALESCE(created_at, '')
FROM entity_edges
WHERE user_name = $2
  AND invalid_at IS NULL
  AND COALESCE(created_at, '') < $1
ORDER BY created_at ASC NULLS FIRST
LIMIT $3`

// FetchStaleEntityEdges returns currently-valid entity_edges older than
// cutoff (ISO timestamp). Used by the periodic bitemporal_validator to
// re-check facts that have aged past the configured staleness window.
func (p *Postgres) FetchStaleEntityEdges(
	ctx context.Context, cutoff, userName string, limit int,
) ([]BiTemporalEdgeRef, error) {
	if cutoff == "" || userName == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, StaleActiveEntityEdgesSQL, cutoff, userName, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch stale entity edges: %w", err)
	}
	defer rows.Close()

	var out []BiTemporalEdgeRef
	for rows.Next() {
		var r BiTemporalEdgeRef
		if err := rows.Scan(&r.FromID, &r.ToID, &r.Predicate, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan stale entity edge: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale entity edges: %w", err)
	}
	return out, nil
}
