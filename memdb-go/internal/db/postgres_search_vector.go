package db

// postgres_search_vector.go — vector similarity search operations.
// Covers: VectorSearchResult type, FormatVector/ParseVectorString helpers,
// VectorSearch, VectorSearchMultiCube, VectorSearchWithCutoff.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
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
func (p *Postgres) VectorSearch(ctx context.Context, vector []float32, cubeID, personID string, memoryTypes []string, agentID string, limit int) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearch, graphName)
	rows, err := p.pool.Query(ctx, q, vecStr, cubeID, personID, memoryTypes, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.Score, &r.EmbeddingStr); err != nil {
			return nil, fmt.Errorf("vector search scan: %w", err)
		}
		r.Embedding = ParseVectorString(r.EmbeddingStr)
		results = append(results, r)
	}
	return results, rows.Err()
}

// VectorSearchMultiCube performs cosine similarity search across multiple cubes.
// Used for cross-domain experience memory: a successful flow on
// site A can be found when handling site B if both are in cubeIDs.
// Behaviour is otherwise identical to VectorSearch.
func (p *Postgres) VectorSearchMultiCube(ctx context.Context, vector []float32, cubeIDs []string, personID string, memoryTypes []string, agentID string, limit int) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearchMultiCube, graphName)
	rows, err := p.pool.Query(ctx, q, vecStr, cubeIDs, personID, memoryTypes, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("vector search multi-cube: %w", err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.Score, &r.EmbeddingStr); err != nil {
			return nil, fmt.Errorf("vector search multi-cube scan: %w", err)
		}
		r.Embedding = ParseVectorString(r.EmbeddingStr)
		results = append(results, r)
	}
	return results, rows.Err()
}

// VectorSearchWithCutoff performs VectorSearch with a temporal created_at cutoff.
func (p *Postgres) VectorSearchWithCutoff(ctx context.Context, vector []float32, cubeID, personID string, memoryTypes []string, limit int, cutoff string, agentID string) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearchWithCutoff, graphName)
	rows, err := p.pool.Query(ctx, q, vecStr, cubeID, personID, memoryTypes, limit, cutoff, agentID)
	if err != nil {
		return nil, fmt.Errorf("vector search with cutoff: %w", err)
	}
	defer rows.Close()

	var results []VectorSearchResult
	for rows.Next() {
		var r VectorSearchResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.Score, &r.EmbeddingStr); err != nil {
			return nil, fmt.Errorf("vector search with cutoff scan: %w", err)
		}
		r.Embedding = ParseVectorString(r.EmbeddingStr)
		results = append(results, r)
	}
	return results, rows.Err()
}
