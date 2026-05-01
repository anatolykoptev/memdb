package db

// postgres_search_vector.go — vector similarity search operations.
// Covers: VectorSearchResult type, FormatVector/ParseVectorString helpers,
// VectorSearch, VectorSearchMultiCube, VectorSearchWithCutoff.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
	"github.com/anatolykoptev/memdb/memdb-go/internal/observability"
)

// VectorSearchResult holds a single result from vector or fulltext search.
type VectorSearchResult struct {
	ID           string    // property UUID (table id column = properties->>'id')
	Properties   string    // raw JSON properties
	Score        float64   // similarity/rank score
	EmbeddingStr string    // raw embedding vector as text (e.g. "[0.1,0.2,...]"), empty for fulltext
	Embedding    []float32 // parsed embedding vector, nil for fulltext
}

// float32BitSize is the strconv bit-size argument for 32-bit float formatting.
const float32BitSize = 32

// FormatVector formats a float32 slice as a pgvector string literal: '[0.1,0.2,...]'.
func FormatVector(vec []float32) string {
	// Pre-allocate: '[' + len(vec)*(~9 chars avg) + ']'
	buf := make([]byte, 0, 1+len(vec)*9)
	buf = append(buf, '[')
	for i, v := range vec {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendFloat(buf, float64(v), 'g', -1, float32BitSize)
	}
	buf = append(buf, ']')
	return string(buf)
}

// ParseVectorString parses a pgvector text representation "[0.1,0.2,...]" into []float32.
// Returns nil on empty or malformed input.
func ParseVectorString(s string) []float32 {
	if len(s) < 3 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil
	}
	inner := s[1 : len(s)-1]
	parts := strings.Split(inner, ",")
	vec := make([]float32, 0, len(parts))
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil
		}
		vec = append(vec, float32(f))
	}
	return vec
}

// VectorSearch performs cosine similarity search across multiple memory types.
// Returns results sorted by similarity score (descending).
//
// M12.5: wrapped via observability.MeasureQuery so per-query latency and pool
// acquire wait time land on memdb_db_query_duration_ms{query_name=VectorSearch}
// and memdb_db_pgxpool_acquire_ms{query_name=VectorSearch}.
func (p *Postgres) VectorSearch(ctx context.Context, vector []float32, cubeID, personID string, memoryTypes []string, agentID string, limit int) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearch, graphName)
	results, err := observability.MeasureQuery(ctx, "VectorSearch", p.pool, func(conn *pgxpool.Conn) ([]VectorSearchResult, error) {
		return scanVectorSearchRows(ctx, conn, "VectorSearch", q, vecStr, cubeID, personID, memoryTypes, limit, agentID)
	})
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	return results, nil
}

// scanVectorSearchRows is shared between VectorSearch / multi-cube / cutoff —
// they differ only in args. Bumps memdb_db_rows_scanned_total per call.
func scanVectorSearchRows(ctx context.Context, conn *pgxpool.Conn, queryName, q string, args ...any) ([]VectorSearchResult, error) {
	rows, err := conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []VectorSearchResult
	var scanned int64
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.Score, &r.EmbeddingStr); err != nil {
			return nil, fmt.Errorf("%s scan: %w", queryName, err)
		}
		r.Embedding = ParseVectorString(r.EmbeddingStr)
		results = append(results, r)
		scanned++
	}
	observability.AddRowsScanned(ctx, queryName, scanned)
	return results, rows.Err()
}

// SparseVectorSearch performs SPLADE sparse-vector similarity search using
// pgvector's `<#>` (negative inner product). Mirrors VectorSearch's signature
// and result shape so the search-service hybrid wrapper can fuse dense+sparse
// rankings via RRF without bespoke plumbing. sparseVecLiteral is a pgvector
// sparsevec literal of the form "{i1:v1,i2:v2,...}/30522" (build via
// embedder.FormatSparseVector).
func (p *Postgres) SparseVectorSearch(ctx context.Context, sparseVecLiteral string, cubeID, personID string, memoryTypes []string, agentID string, limit int) ([]VectorSearchResult, error) {
	q := fmt.Sprintf(queries.SparseVectorSearch, graphName)
	results, err := observability.MeasureQuery(ctx, "SparseVectorSearch", p.pool, func(conn *pgxpool.Conn) ([]VectorSearchResult, error) {
		return scanVectorSearchRows(ctx, conn, "SparseVectorSearch", q, sparseVecLiteral, cubeID, personID, memoryTypes, limit, agentID)
	})
	if err != nil {
		return nil, fmt.Errorf("sparse vector search: %w", err)
	}
	return results, nil
}

// VectorSearchMultiCube performs cosine similarity search across multiple cubes.
// Used for cross-domain experience memory: a successful flow on
// site A can be found when handling site B if both are in cubeIDs.
// Behaviour is otherwise identical to VectorSearch.
//
// M12.5: shares the "VectorSearch" query_name label with the single-cube
// VectorSearch — same SQL shape, different args. Splitting the label would
// double dashboard noise without adding signal.
func (p *Postgres) VectorSearchMultiCube(ctx context.Context, vector []float32, cubeIDs []string, personID string, memoryTypes []string, agentID string, limit int) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearchMultiCube, graphName)
	results, err := observability.MeasureQuery(ctx, "VectorSearch", p.pool, func(conn *pgxpool.Conn) ([]VectorSearchResult, error) {
		return scanVectorSearchRows(ctx, conn, "VectorSearch", q, vecStr, cubeIDs, personID, memoryTypes, limit, agentID)
	})
	if err != nil {
		return nil, fmt.Errorf("vector search multi-cube: %w", err)
	}
	return results, nil
}

// VectorSearchWithCutoff performs VectorSearch with a temporal created_at cutoff.
func (p *Postgres) VectorSearchWithCutoff(ctx context.Context, vector []float32, cubeID, personID string, memoryTypes []string, limit int, cutoff string, agentID string) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearchWithCutoff, graphName)
	results, err := observability.MeasureQuery(ctx, "VectorSearch", p.pool, func(conn *pgxpool.Conn) ([]VectorSearchResult, error) {
		return scanVectorSearchRows(ctx, conn, "VectorSearch", q, vecStr, cubeID, personID, memoryTypes, limit, cutoff, agentID)
	})
	if err != nil {
		return nil, fmt.Errorf("vector search with cutoff: %w", err)
	}
	return results, nil
}
