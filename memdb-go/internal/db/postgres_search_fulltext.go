package db

// postgres_search_fulltext.go — fulltext (tsvector) search operations.
// Covers: FulltextSearch, FulltextSearchWithCutoff.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// FulltextSearch performs tsvector fulltext search across multiple memory types.
// The tsquery should be pre-built (e.g. "token1 | token2 | token3").
func (p *Postgres) FulltextSearch(ctx context.Context, tsquery string, cubeID, personID string, memoryTypes []string, agentID string, limit int) ([]VectorSearchResult, error) {
	if tsquery == "" {
		return nil, nil
	}
	q := fmt.Sprintf(queries.FulltextSearch, graphName)
	rows, err := p.pool.Query(ctx, q, tsquery, cubeID, personID, memoryTypes, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("fulltext search: %w", err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.Score); err != nil {
			return nil, fmt.Errorf("fulltext search scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// FulltextSearchWithCutoff performs FulltextSearch with a temporal created_at cutoff.
func (p *Postgres) FulltextSearchWithCutoff(ctx context.Context, tsquery string, cubeID, personID string, memoryTypes []string, limit int, cutoff string, agentID string) ([]VectorSearchResult, error) {
	if tsquery == "" {
		return nil, nil
	}
	q := fmt.Sprintf(queries.FulltextSearchWithCutoff, graphName)
	rows, err := p.pool.Query(ctx, q, tsquery, cubeID, personID, memoryTypes, limit, cutoff, agentID)
	if err != nil {
		return nil, fmt.Errorf("fulltext search with cutoff: %w", err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.Score); err != nil {
			return nil, fmt.Errorf("fulltext search with cutoff scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
