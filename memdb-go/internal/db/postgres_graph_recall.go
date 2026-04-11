package db

// postgres_graph_recall.go — graph-based memory recall operations.
// Covers: GraphRecallResult type, GraphRecallByEdge, GraphRecallByKey,
// GraphRecallByTags, GraphBFSTraversal.

import (
	"context"
	"fmt"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// GraphRecallResult holds a single result from graph-based recall.
type GraphRecallResult struct {
	ID         string // property UUID (table id column = properties->>'id')
	Properties string // raw JSON properties
	TagOverlap int    // number of overlapping tags (0 for key-based recall)
}

// GraphRecallByEdge returns memory nodes reachable from seed IDs via directed edges of a given relation.
func (p *Postgres) GraphRecallByEdge(ctx context.Context, seedIDs []string, relation, cubeID, personID string, limit int) ([]GraphRecallResult, error) {
	if len(seedIDs) == 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.GraphRecallByEdge, graphName)
	rows, err := p.pool.Query(ctx, q, seedIDs, relation, cubeID, personID, limit)
	if err != nil {
		return nil, fmt.Errorf("graph recall by edge: %w", err)
	}
	defer rows.Close()

	var results []GraphRecallResult
	for rows.Next() {
		var r GraphRecallResult
		if err := rows.Scan(&r.ID, &r.Properties); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GraphRecallByKey finds nodes where properties->>'key' matches any given key.
func (p *Postgres) GraphRecallByKey(ctx context.Context, cubeID, personID string, memoryTypes []string, keys []string, agentID string, limit int) ([]GraphRecallResult, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.GraphRecallByKey, graphName)
	rows, err := p.pool.Query(ctx, q, cubeID, personID, memoryTypes, keys, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("graph recall by key: %w", err)
	}
	defer rows.Close()

	var results []GraphRecallResult
	for rows.Next() {
		var r GraphRecallResult
		if err := rows.Scan(&r.ID, &r.Properties); err != nil {
			return nil, fmt.Errorf("graph recall by key scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GraphRecallByTags finds nodes with >= 2 overlapping tags.
func (p *Postgres) GraphRecallByTags(ctx context.Context, cubeID, personID string, memoryTypes []string, tags []string, agentID string, limit int) ([]GraphRecallResult, error) {
	if len(tags) < 2 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.GraphRecallByTags, graphName)
	rows, err := p.pool.Query(ctx, q, cubeID, personID, memoryTypes, tags, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("graph recall by tags: %w", err)
	}
	defer rows.Close()

	var results []GraphRecallResult
	for rows.Next() {
		var r GraphRecallResult
		if err := rows.Scan(&r.ID, &r.Properties, &r.TagOverlap); err != nil {
			return nil, fmt.Errorf("graph recall by tags scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// GraphBFSTraversal expands seed node IDs up to `depth` hops via working_binding relationships.
// Returns neighboring nodes not already in the seed set.
func (p *Postgres) GraphBFSTraversal(ctx context.Context, seedIDs []string, cubeID, personID string, memoryTypes []string, depth, limit int, agentID string) ([]GraphRecallResult, error) {
	if len(seedIDs) == 0 || depth <= 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.GraphBFSTraversal, graphName)
	rows, err := p.pool.Query(ctx, q, seedIDs, cubeID, personID, memoryTypes, depth, limit, agentID)
	if err != nil {
		return nil, fmt.Errorf("graph bfs traversal: %w", err)
	}
	defer rows.Close()

	var results []GraphRecallResult
	for rows.Next() {
		var r GraphRecallResult
		if err := rows.Scan(&r.ID, &r.Properties); err != nil {
			return nil, fmt.Errorf("graph bfs scan: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
