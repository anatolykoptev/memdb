package db

// postgres_search.go — vector and fulltext search operations.
// Covers: VectorSearch, FulltextSearch (with/without temporal cutoff),
// helper types VectorSearchResult, and vector formatting utilities.

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
func (p *Postgres) VectorSearch(ctx context.Context, vector []float32, userName string, memoryTypes []string, agentID string, limit int) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearch, graphName)
	rows, err := p.pool.Query(ctx, q, vecStr, userName, memoryTypes, limit, agentID)
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

// VectorSearchMultiCube performs cosine similarity search across multiple cubes
// (user_names). Used for cross-domain experience memory: a successful flow on
// site A can be found when handling site B if both are in userNames.
// Behaviour is otherwise identical to VectorSearch.
func (p *Postgres) VectorSearchMultiCube(ctx context.Context, vector []float32, userNames []string, memoryTypes []string, agentID string, limit int) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearchMultiCube, graphName)
	rows, err := p.pool.Query(ctx, q, vecStr, userNames, memoryTypes, limit, agentID)
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

// FulltextSearch performs tsvector fulltext search across multiple memory types.
// The tsquery should be pre-built (e.g. "token1 | token2 | token3").
func (p *Postgres) FulltextSearch(ctx context.Context, tsquery string, userName string, memoryTypes []string, agentID string, limit int) ([]VectorSearchResult, error) {
	if tsquery == "" {
		return nil, nil
	}
	q := fmt.Sprintf(queries.FulltextSearch, graphName)
	rows, err := p.pool.Query(ctx, q, tsquery, userName, memoryTypes, limit, agentID)
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

// VectorSearchWithCutoff performs VectorSearch with a temporal created_at cutoff.
func (p *Postgres) VectorSearchWithCutoff(ctx context.Context, vector []float32, userName string, memoryTypes []string, limit int, cutoff string, agentID string) ([]VectorSearchResult, error) {
	vecStr := FormatVector(vector)
	q := fmt.Sprintf(queries.VectorSearchWithCutoff, graphName)
	rows, err := p.pool.Query(ctx, q, vecStr, userName, memoryTypes, limit, cutoff, agentID)
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

// FulltextSearchWithCutoff performs FulltextSearch with a temporal created_at cutoff.
func (p *Postgres) FulltextSearchWithCutoff(ctx context.Context, tsquery string, userName string, memoryTypes []string, limit int, cutoff string, agentID string) ([]VectorSearchResult, error) {
	if tsquery == "" {
		return nil, nil
	}
	q := fmt.Sprintf(queries.FulltextSearchWithCutoff, graphName)
	rows, err := p.pool.Query(ctx, q, tsquery, userName, memoryTypes, limit, cutoff, agentID)
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

// GetWorkingMemory returns all activated WorkingMemory items for a user, ordered by recency.
// Returns embeddings so callers can compute cosine similarity against the query vector.
func (p *Postgres) GetWorkingMemory(ctx context.Context, userName string, limit int, agentID string) ([]VectorSearchResult, error) {
	q := fmt.Sprintf(queries.GetWorkingMemory, graphName)
	rows, err := p.pool.Query(ctx, q, userName, limit, agentID)
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
