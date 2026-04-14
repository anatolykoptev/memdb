package db

// postgres_memory_ltm.go — LongTermMemory vector search and near-duplicate detection.
// Covers: LTM vector search, dedup pairs (full scan + ID-scoped), soft-delete-merged.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
	"github.com/jackc/pgx/v5"
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
// Wrapped in a read-only tx with SET LOCAL statement_timeout = '120s' to avoid the role-level
// default (30s) being hit by the O(N²) self-join on the embedding column.
func (p *Postgres) FindNearDuplicates(ctx context.Context, userName string, threshold float64, limit int) ([]DuplicatePair, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("find near duplicates begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only tx, rollback error is irrelevant

	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '120s'"); err != nil {
		return nil, fmt.Errorf("find near duplicates set timeout: %w", err)
	}

	rows, err := tx.Query(ctx, fmt.Sprintf(queries.FindNearDuplicates, graphName), userName, threshold, limit)
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
// Wrapped in a read-only tx with SET LOCAL statement_timeout = '120s' (same reason as FindNearDuplicates).
func (p *Postgres) FindNearDuplicatesByIDs(ctx context.Context, userName string, ids []string, threshold float64, limit int) ([]DuplicatePair, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("find near duplicates by ids begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only tx, rollback error is irrelevant

	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '120s'"); err != nil {
		return nil, fmt.Errorf("find near duplicates by ids set timeout: %w", err)
	}

	rows, err := tx.Query(ctx, fmt.Sprintf(queries.FindNearDuplicatesByIDs, graphName), userName, ids, threshold, limit)
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

// FindNearDuplicatesHNSW is the HNSW-indexed variant of FindNearDuplicates.
// Uses CROSS JOIN LATERAL with the existing idx_memory_embedding HNSW index.
// topK is the per-memory candidate pool (recommended: 20). Approximate —
// SET LOCAL hnsw.ef_search=100 is applied to raise recall above the index default.
//
// Same 120s statement_timeout wrapper as the legacy FindNearDuplicates.
func (p *Postgres) FindNearDuplicatesHNSW(ctx context.Context, userName string, threshold float64, limit, topK int) ([]DuplicatePair, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("find near duplicates hnsw begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only tx, rollback error is irrelevant

	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '120s'"); err != nil {
		return nil, fmt.Errorf("find near duplicates hnsw set timeout: %w", err)
	}
	if _, err = tx.Exec(ctx, "SET LOCAL hnsw.ef_search = 100"); err != nil {
		return nil, fmt.Errorf("find near duplicates hnsw set ef_search: %w", err)
	}

	rows, err := tx.Query(ctx, fmt.Sprintf(queries.FindNearDuplicatesHNSW, graphName), userName, threshold, limit, topK)
	if err != nil {
		return nil, fmt.Errorf("find near duplicates hnsw: %w", err)
	}
	defer rows.Close()
	var pairs []DuplicatePair
	for rows.Next() {
		var dp DuplicatePair
		if err := rows.Scan(&dp.IDa, &dp.MemA, &dp.IDb, &dp.MemB, &dp.Score); err != nil {
			return nil, fmt.Errorf("find near duplicates hnsw scan: %w", err)
		}
		pairs = append(pairs, dp)
	}
	return pairs, rows.Err()
}

// FindNearDuplicatesHNSWByIDs is the HNSW variant of FindNearDuplicatesByIDs.
// Restricts the scan to pairs where at least one node is in ids.
func (p *Postgres) FindNearDuplicatesHNSWByIDs(ctx context.Context, userName string, ids []string, threshold float64, limit, topK int) ([]DuplicatePair, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("find near duplicates hnsw by ids begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only tx, rollback error is irrelevant

	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '120s'"); err != nil {
		return nil, fmt.Errorf("find near duplicates hnsw by ids set timeout: %w", err)
	}
	if _, err = tx.Exec(ctx, "SET LOCAL hnsw.ef_search = 100"); err != nil {
		return nil, fmt.Errorf("find near duplicates hnsw by ids set ef_search: %w", err)
	}

	rows, err := tx.Query(ctx, fmt.Sprintf(queries.FindNearDuplicatesHNSWByIDs, graphName), userName, ids, threshold, limit, topK)
	if err != nil {
		return nil, fmt.Errorf("find near duplicates hnsw by ids: %w", err)
	}
	defer rows.Close()
	var pairs []DuplicatePair
	for rows.Next() {
		var dp DuplicatePair
		if err := rows.Scan(&dp.IDa, &dp.MemA, &dp.IDb, &dp.MemB, &dp.Score); err != nil {
			return nil, fmt.Errorf("find near duplicates hnsw by ids scan: %w", err)
		}
		pairs = append(pairs, dp)
	}
	return pairs, rows.Err()
}

// FindNearDuplicatesHNSWSQL returns the HNSW SQL template for testing.
func FindNearDuplicatesHNSWSQL() string { return queries.FindNearDuplicatesHNSW }

// FindNearDuplicatesHNSWByIDsSQL returns the HNSW ByIDs SQL template for testing.
func FindNearDuplicatesHNSWByIDsSQL() string { return queries.FindNearDuplicatesHNSWByIDs }

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
