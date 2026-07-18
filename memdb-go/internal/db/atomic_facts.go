// Package db — atomic_facts.go: top-K cosine retrieval of atomic facts for the
// chat "## Key Facts" prompt section (Memobase parity, Path X 2026-05-01).
//
// Why a dedicated read path:
//   - The generic VectorSearch competes paragraph_legacy AND atomic_fact rows
//     in one ORDER BY embedding<=>$1. Atomic facts are SHORT (one sentence) so
//     their embeddings tend to score lower cosine vs verbose paragraph rows on
//     longer queries — the gold atomic surfaces but loses ranking.
//   - Memobase ships atomic facts inlined in the system prompt as a guaranteed
//     "## Key Facts" block, NOT as rerank candidates. To mirror that we need a
//     thin direct SQL probe scoped to kind='atomic_fact' so the prompt-section
//     renderer can emit them deterministically per query.
//
// Mirrors the WHERE shape used by NearestAtomicFact (see postgres_memory_dedup.go),
// extended to (a) support multi-cube fan-out and (b) return attributed_to /
// event_dates (lifted top-level by add_fine_atomic_persist.liftAtomicDiscriminators).
package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
)

// AtomicFactRow — minimal projection used by the chat "## Key Facts" prompt
// section. Only the fields the renderer needs are scanned:
//   - Memory:        the fact text body (properties->>'memory').
//   - AttributedTo:  optional speaker attribution (properties->>'attributed_to'),
//     e.g. "Caroline" / "Melanie" — load-bearing in dual-speaker
//     chats so the LLM can cross-reference the right person.
//   - EventDates:    optional ISO-8601 calendar anchors
//     (properties->'event_dates'::jsonb array). The renderer
//     surfaces only the first element to keep the prompt tight;
//     callers needing the full set can read the column directly.
//   - Score:         cosine similarity in [0, 1] (1 - <=> distance), ordered DESC.
type AtomicFactRow struct {
	Memory       string
	AttributedTo string
	EventDates   []string
	Score        float64
}

// GetTopAtomicFactsByCosine returns the top-K atomic facts ranked by cosine
// similarity to queryVec across the supplied cubes. The query is restricted
// to LongTermMemory rows with kind='atomic_fact' and a non-NULL embedding —
// the same dedupe scope used by NearestAtomicFact, so chat-side reads see
// the same fact universe ingest writes.
//
// Returns (nil, nil) when:
//   - p / pool / cubeIDs / queryVec is empty (no cube to scope, no vec to score)
//   - topK <= 0 (callers MUST pass a positive limit; we don't pick a default
//     here because the prompt budget is owned by the caller)
//   - the cube has zero atomic facts yet (cold cube)
//
// Errors are wrapped with the cube list shape; callers in the chat hot path
// should log + swallow (best-effort prompt enrichment must never block chat).
//
// AGE caveat: properties is agtype, not jsonb. Direct ->> reads error out
// (see postgres_memory_dedup.go:50 forensic). All reads cast through
// `properties::text::jsonb` to match the production query shape.
func (p *Postgres) GetTopAtomicFactsByCosine(
	ctx context.Context, cubeIDs []string, queryVec []float32, topK int,
) ([]AtomicFactRow, error) {
	if p == nil || p.pool == nil || len(cubeIDs) == 0 || len(queryVec) == 0 || topK <= 0 {
		return nil, nil
	}
	vecStr := FormatVector(queryVec)
	const q = `
SELECT (properties::text::jsonb)->>'memory'                              AS memory,
       COALESCE((properties::text::jsonb)->>'attributed_to', '')         AS attributed_to,
       COALESCE((properties::text::jsonb)->'event_dates', '[]'::jsonb)   AS event_dates,
       1 - (embedding::halfvec(1024) <=> $2::halfvec(1024))              AS score
FROM   memos_graph."Memory"
WHERE  (properties::text::jsonb)->>'user_name'   = ANY($1::text[])
  AND  (properties::text::jsonb)->>'memory_type' = 'LongTermMemory'
  AND  (properties::text::jsonb)->>'kind'        = 'atomic_fact'
  AND  embedding IS NOT NULL
ORDER  BY embedding::halfvec(1024) <=> $2::halfvec(1024)
LIMIT  $3`
	rows, err := observability.MeasureQuery(ctx, "GetTopAtomicFactsByCosine", p.pool, func(conn *pgxpool.Conn) ([]AtomicFactRow, error) {
		r, qerr := conn.Query(ctx, q, cubeIDs, vecStr, topK)
		if qerr != nil {
			return nil, qerr
		}
		defer r.Close()
		out := make([]AtomicFactRow, 0, topK)
		var scanned int64
		for r.Next() {
			var row AtomicFactRow
			var datesRaw []byte
			if err := r.Scan(&row.Memory, &row.AttributedTo, &datesRaw, &row.Score); err != nil {
				return nil, fmt.Errorf("GetTopAtomicFactsByCosine scan: %w", err)
			}
			if len(datesRaw) > 0 {
				// Tolerate malformed event_dates rather than failing the whole
				// probe — the prompt section is best-effort enrichment.
				_ = json.Unmarshal(datesRaw, &row.EventDates)
			}
			out = append(out, row)
			scanned++
		}
		observability.AddRowsScanned(ctx, "GetTopAtomicFactsByCosine", scanned)
		return out, r.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("GetTopAtomicFactsByCosine (cubes=%d): %w", len(cubeIDs), err)
	}
	return rows, nil
}
