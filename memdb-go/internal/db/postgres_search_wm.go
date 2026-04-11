package db

// postgres_search_wm.go — working memory fetch and importance lifecycle operations.
// Covers: GetWorkingMemory, IncrRetrievalCount, DecayAndArchiveImportance.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// GetWorkingMemory returns all activated WorkingMemory items for a user, ordered by recency.
// Returns embeddings so callers can compute cosine similarity against the query vector.
func (p *Postgres) GetWorkingMemory(ctx context.Context, cubeID, personID string, limit int, agentID string) ([]VectorSearchResult, error) {
	q := fmt.Sprintf(queries.GetWorkingMemory, graphName)
	rows, err := p.pool.Query(ctx, q, cubeID, personID, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("get working memory: %w", err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		var embStr string
		if err := rows.Scan(&r.ID, &r.Properties, &embStr); err != nil {
			return nil, fmt.Errorf("get working memory scan: %w", err)
		}
		r.Embedding = ParseVectorString(embStr)
		results = append(results, r)
	}
	return results, rows.Err()
}

// IncrRetrievalCount increments retrieval_count and boosts importance_score (+0.1, max 2.0)
// for a batch of memory nodes. Intended to be called asynchronously (fire-and-forget).
func (p *Postgres) IncrRetrievalCount(ctx context.Context, ids []string, now string) error {
	if len(ids) == 0 {
		return nil
	}
	q := fmt.Sprintf(queries.IncrRetrievalCount, graphName)
	_, err := p.pool.Exec(ctx, q, ids, now)
	if err != nil {
		return fmt.Errorf("incr retrieval count: %w", err)
	}
	return nil
}

// DecayAndArchiveImportance runs the two-phase importance lifecycle for a user's LTM:
//  1. Multiply every activated LTM/UserMemory importance_score by decayFactor (e.g. 0.95)
//  2. Archive nodes whose score drops below archiveThreshold (e.g. 0.1)
//
// Returns the number of nodes archived.
// Intended to be called from the periodic reorganization loop (every ~6h per cube).
func (p *Postgres) DecayAndArchiveImportance(ctx context.Context, userName string, decayFactor, archiveThreshold float64, now string) (int64, error) {
	decayQ := fmt.Sprintf(queries.DecayImportanceScores, graphName)
	if _, err := p.pool.Exec(ctx, decayQ, userName); err != nil {
		return 0, fmt.Errorf("decay importance scores: %w", err)
	}

	archiveQ := fmt.Sprintf(queries.AutoArchiveLowImportance, graphName)
	tag, err := p.pool.Exec(ctx, archiveQ, userName, archiveThreshold, now)
	if err != nil {
		return 0, fmt.Errorf("auto archive low importance: %w", err)
	}
	return tag.RowsAffected(), nil
}
