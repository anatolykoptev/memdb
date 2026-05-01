// Package db — postgres_memory_dedup.go: per-fact pgvector similarity probe
// used by handlers.dedupAtomicFactsAgainstCube.
//
// Why: the upstream extractor (Gemini Flash) emits paraphrase duplicates
// across reverse-role passes — same event extracted 5+ times in slightly
// different wording. Prompt-side "no within-response duplication" rules don't
// span calls. mem0's audit (issue #4573) saw 37.6% near-duplicates remain in
// production. Zep/Graphiti documented the pattern: dedupe at persist time
// using embedding cosine similarity, NOT prompt instructions.
//
// This file ships the cheap kNN lookup. The decision logic (threshold,
// merge vs skip) lives in the handlers package next to the persist hook.
package db

import (
	"context"
	"fmt"
)

// SimilarMemoryHit is the minimal projection returned by NearestAtomicFact.
// We deliberately don't return the full memoryRow shape — the persist hook
// only needs the similarity score and the matched memory text for logging.
type SimilarMemoryHit struct {
	ID         string
	Memory     string
	Similarity float64 // 0..1, cosine; higher = more similar
}

// NearestAtomicFact returns the single closest atomic-fact LongTermMemory in
// the cube, ranked by cosine similarity against `query`. Returns
// (nil, nil) if there are no atomic facts in the cube yet (cold cube — first
// fact going in is always unique).
//
// Why scoped to atomic_fact + LongTermMemory:
//   - Working-memory rows are duplicates of LTM by design (background-binding
//     pattern), so the same fact would appear twice in the search and both
//     hits would say "perfectly similar".
//   - paragraph_legacy rows aren't atomic facts; matching against them would
//     cross dedupe scopes and produce surprising drops.
//
// HNSW halfvec index pattern mirrors SearchEventsByCosine. NULL embeddings
// are excluded so half-extracted rows don't poison the ranking.
func (p *Postgres) NearestAtomicFact(
	ctx context.Context, cubeID string, query []float32,
) (*SimilarMemoryHit, error) {
	if p == nil || p.pool == nil || cubeID == "" || len(query) == 0 {
		return nil, nil
	}
	vecStr := FormatVector(query)
	// 2026-05-01 gotcha: memos_graph."Memory".properties is Apache AGE
	// agtype, NOT jsonb. Direct ->> reads error out (probe_error metric
	// fired 246/246 on first wiring). The workaround mirrors every other
	// production query that touches this column — cast through text first:
	// `properties::text::jsonb->>'…'`. See [age_migration_gotcha.md].
	const q = `
SELECT id::text,
       (properties::text::jsonb)->>'memory'                             AS memory,
       1 - (embedding::halfvec(1024) <=> $2::halfvec(1024))             AS sim
FROM   memos_graph."Memory"
WHERE  (properties::text::jsonb)->>'user_name'   = $1
  AND  (properties::text::jsonb)->>'memory_type' = 'LongTermMemory'
  AND  (properties::text::jsonb)->>'kind'        = 'atomic_fact'
  AND  embedding IS NOT NULL
ORDER  BY embedding::halfvec(1024) <=> $2::halfvec(1024)
LIMIT  1`
	rows, err := p.pool.Query(ctx, q, cubeID, vecStr)
	if err != nil {
		return nil, fmt.Errorf("NearestAtomicFact query: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, nil
	}
	hit := &SimilarMemoryHit{}
	if err := rows.Scan(&hit.ID, &hit.Memory, &hit.Similarity); err != nil {
		return nil, fmt.Errorf("NearestAtomicFact scan: %w", err)
	}
	return hit, nil
}
