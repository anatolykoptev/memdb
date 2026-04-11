package db

// postgres_memory_ltm.go — LongTermMemory vector search and near-duplicate detection.
// Covers: LTM vector search, dedup pairs (full scan + ID-scoped), soft-delete-merged.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// SearchLTMByVector returns top-k activated LTM/UserMemory nodes by cosine similarity.
func (p *Postgres) SearchLTMByVector(ctx context.Context, userName, embeddingVec string, minScore float64, limit int) ([]LTMSearchResult, error) {
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.SearchLTMByVector, graphName), userName, embeddingVec, minScore, limit)
	if err != nil {
		return nil, fmt.Errorf("search ltm by vector: %w", err)
	}
	defer rows.Close()
	var results []LTMSearchResult
	for rows.Next() {
		var r LTMSearchResult
		var embText string
		if err := rows.Scan(&r.ID, &r.Text, &r.Score, &embText); err != nil {
			return nil, fmt.Errorf("search ltm scan: %w", err)
		}
		r.Embedding = ParseVectorString(embText)
		results = append(results, r)
	}
	return results, rows.Err()
}

// FindNearDuplicates returns activated LTM/UserMemory pairs with cosine similarity >= threshold.
func (p *Postgres) FindNearDuplicates(ctx context.Context, userName string, threshold float64, limit int) ([]DuplicatePair, error) {
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.FindNearDuplicates, graphName), userName, threshold, limit)
	if err != nil {
		return nil, fmt.Errorf("find near duplicates: %w", err)
	}
	defer rows.Close()
	var pairs []DuplicatePair
	for rows.Next() {
		var dp DuplicatePair
		if err := rows.Scan(&dp.IDa, &dp.MemA, &dp.IDb, &dp.MemB, &dp.Score); err != nil {
			return nil, fmt.Errorf("find near duplicates scan: %w", err)
		}
		pairs = append(pairs, dp)
	}
	return pairs, rows.Err()
}

// FindNearDuplicatesByIDs restricts near-duplicate scan to pairs where at least one node is in ids.
func (p *Postgres) FindNearDuplicatesByIDs(ctx context.Context, userName string, ids []string, threshold float64, limit int) ([]DuplicatePair, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := p.pool.Query(ctx, fmt.Sprintf(queries.FindNearDuplicatesByIDs, graphName), userName, ids, threshold, limit)
	if err != nil {
		return nil, fmt.Errorf("find near duplicates by ids: %w", err)
	}
	defer rows.Close()
	var pairs []DuplicatePair
	for rows.Next() {
		var dp DuplicatePair
		if err := rows.Scan(&dp.IDa, &dp.MemA, &dp.IDb, &dp.MemB, &dp.Score); err != nil {
			return nil, fmt.Errorf("find near duplicates by ids scan: %w", err)
		}
		pairs = append(pairs, dp)
	}
	return pairs, rows.Err()
}

// SoftDeleteMerged marks a memory as merged (activated → merged lifecycle transition).
func (p *Postgres) SoftDeleteMerged(ctx context.Context, memoryID, mergedIntoID, updatedAt string) error {
	_, err := p.pool.Exec(ctx, fmt.Sprintf(queries.SoftDeleteMerged, graphName), memoryID, mergedIntoID, updatedAt)
	if err != nil {
		return fmt.Errorf("soft delete merged %s: %w", memoryID, err)
	}
	return nil
}

// SearchLTMByVectorSQL returns the SearchLTMByVector SQL template for testing.
func SearchLTMByVectorSQL() string { return queries.SearchLTMByVector }

// FindNearDuplicatesSQL returns the FindNearDuplicates SQL template for testing.
func FindNearDuplicatesSQL() string { return queries.FindNearDuplicates }
