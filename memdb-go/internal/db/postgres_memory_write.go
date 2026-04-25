package db

// postgres_memory_write.go — memory node write operations.
// Covers: delete, update content, update full (props+embedding), delete-all,
//         PageRank edge fetch + bulk score persist (M10 Stream 7).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// PageRankEdge is a directed edge record returned by FetchEdgesForPageRank.
// Weight is the edge confidence (0..1); 0 means uniform (treated as 1.0 in PageRank).
type PageRankEdge struct {
	FromID string
	ToID   string
	Weight float64
}

// ErrMemoryNotFound is returned by UpdateMemoryByID when the target row does not exist.
// This is a normal condition when the Reorganizer hard-deleted a contradicted memory
// between the caller's search and the update call.
var ErrMemoryNotFound = errors.New("memory not found")

// DeleteByPropertyIDs deletes nodes matching the given property IDs and user name.
func (p *Postgres) DeleteByPropertyIDs(ctx context.Context, propertyIDs []string, userName string) (int64, error) {
	tag, err := p.pool.Exec(ctx, fmt.Sprintf(queries.DeleteByPropertyIDs, graphName), propertyIDs, userName)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// UpdateMemoryContent updates the memory text for a given memory node.
func (p *Postgres) UpdateMemoryContent(ctx context.Context, memoryID, content string) (bool, error) {
	tag, err := p.pool.Exec(ctx, fmt.Sprintf(queries.UpdateMemoryContent, graphName), memoryID, content)
	if err != nil {
		return false, fmt.Errorf("update memory: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteAllByUser deletes all activated memories for a user.
func (p *Postgres) DeleteAllByUser(ctx context.Context, userName string) (int64, error) {
	tag, err := p.pool.Exec(ctx, fmt.Sprintf(queries.DeleteAllByUser, graphName), userName)
	if err != nil {
		return 0, fmt.Errorf("delete all by user: %w", err)
	}
	return tag.RowsAffected(), nil
}

// UpdateMemoryByID atomically replaces properties + embedding for a memory node.
func (p *Postgres) UpdateMemoryByID(ctx context.Context, memoryID, cubeID string, propsJSON []byte, embedding string) error {
	var returnedID string
	err := p.pool.QueryRow(ctx, fmt.Sprintf(queries.UpdateMemoryPropsAndEmbedding, graphName), memoryID, cubeID, propsJSON, embedding).Scan(&returnedID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%s cube=%s", ErrMemoryNotFound, memoryID, cubeID)
		}
		return fmt.Errorf("update memory: %w", err)
	}
	return nil
}

// CEScoreEntry is a single pre-computed cross-encoder pair score persisted
// inside Memory.properties->>'ce_score_topk'. NeighborID is the partner
// memory's UUID; Score is the BGE-reranker-v2-m3 relevance score in
// roughly [0, 1] (higher = more semantically related).
type CEScoreEntry struct {
	NeighborID string  `json:"neighbor_id"`
	Score      float32 `json:"score"`
}

// SetCEScoresTopK persists pre-computed top-K cross-encoder scores for a
// single memory under properties->>'ce_score_topk'. Caller is responsible
// for sorting entries by Score DESC. Single round-trip — no read-modify-
// write race. Returns nil even when no row matches (the memory may have
// been deleted between the D3 pass starting and this UPDATE landing).
func (p *Postgres) SetCEScoresTopK(ctx context.Context, memoryID, cubeID string, entries []CEScoreEntry) error {
	if memoryID == "" || cubeID == "" {
		return nil
	}
	if entries == nil {
		entries = []CEScoreEntry{}
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal ce_score_topk: %w", err)
	}
	if _, err := p.pool.Exec(ctx, fmt.Sprintf(queries.SetCEScoresTopK, graphName), memoryID, cubeID, string(body)); err != nil {
		return fmt.Errorf("set ce_score_topk: %w", err)
	}
	return nil
}

// ClearCEScoresTopK removes the ce_score_topk key on a single memory by
// UUID alone. Called from applyUpdateAction when the memory's text
// changes (cached pairwise scores no longer reflect the new content).
func (p *Postgres) ClearCEScoresTopK(ctx context.Context, memoryID string) error {
	if memoryID == "" {
		return nil
	}
	if _, err := p.pool.Exec(ctx, fmt.Sprintf(queries.ClearCEScoresTopK, graphName), memoryID); err != nil {
		return fmt.Errorf("clear ce_score_topk: %w", err)
	}
	return nil
}

// ClearCEScoresTopKForNeighbor cascades cache invalidation: clears
// ce_score_topk on any memory that listed neighborID inside its top-K
// array. Run after applyUpdateAction / applyDeleteAction so dangling
// cached scores against the affected memory disappear in a single SQL
// statement.
func (p *Postgres) ClearCEScoresTopKForNeighbor(ctx context.Context, neighborID string) error {
	if neighborID == "" {
		return nil
	}
	if _, err := p.pool.Exec(ctx, fmt.Sprintf(queries.ClearCEScoresTopKForNeighbor, graphName), neighborID); err != nil {
		return fmt.Errorf("clear ce_score_topk for neighbor: %w", err)
	}
	return nil
}

// FetchEdgesForPageRank returns all currently-valid memory_edges for the given
// cube (identified by cube_id / user_name stored in Memory.properties).
// Only edges whose both endpoints belong to the cube are returned.
// Weight is taken from the confidence column; NULL confidence maps to 0
// (the PageRank engine treats 0 as uniform weight 1.0).
func (p *Postgres) FetchEdgesForPageRank(ctx context.Context, cubeID string) ([]PageRankEdge, error) {
	const q = `
SELECT e.from_id, e.to_id, COALESCE(e.confidence, 0)
FROM memory_edges e
WHERE e.invalid_at IS NULL
  AND EXISTS (
      SELECT 1 FROM %[1]s."Memory" m
      WHERE m.properties->>(('id'::text)) = e.from_id
        AND m.properties->>(('user_name'::text)) = $1
        AND m.properties->>(('status'::text)) = 'activated'
  )`
	rows, err := p.pool.Query(ctx, fmt.Sprintf(q, graphName), cubeID)
	if err != nil {
		return nil, fmt.Errorf("fetch edges for pagerank cube=%s: %w", cubeID, err)
	}
	defer rows.Close()

	var out []PageRankEdge
	for rows.Next() {
		var e PageRankEdge
		if err := rows.Scan(&e.FromID, &e.ToID, &e.Weight); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BulkSetPageRank persists PageRank scores into Memory.properties->>'pagerank'
// for all nodes of a cube. Single bulk UPDATE round-trip using UNNEST.
// Scores outside [0, 1] are clamped silently. Memory rows not in the scores
// map are left unchanged (they keep whatever pagerank they had or none).
func (p *Postgres) BulkSetPageRank(ctx context.Context, cubeID string, scores map[string]float64) error {
	if len(scores) == 0 {
		return nil
	}

	ids := make([]string, 0, len(scores))
	vals := make([]string, 0, len(scores))
	now := time.Now().UTC().Format(time.RFC3339)
	for id, s := range scores {
		if id == "" {
			continue
		}
		if s < 0 {
			s = 0
		} else if s > 1 {
			s = 1
		}
		ids = append(ids, id)
		vals = append(vals, strconv.FormatFloat(s, 'f', 8, 64))
	}
	if len(ids) == 0 {
		return nil
	}

	const q = `
UPDATE %[1]s."Memory"
SET properties = (
        (properties::text::jsonb || jsonb_build_object(
            'pagerank',  u.score::text,
            'updated_at', $3::text
        ))::text
    )::agtype
FROM UNNEST($1::text[], $2::text[]) AS u(mem_id, score)
WHERE properties->>(('id'::text)) = u.mem_id
  AND properties->>(('user_name'::text)) = $4
  AND properties->>(('status'::text)) = 'activated'`

	_, err := p.pool.Exec(ctx, fmt.Sprintf(q, graphName), ids, vals, now, cubeID)
	if err != nil {
		return fmt.Errorf("bulk set pagerank cube=%s: %w", cubeID, err)
	}
	return nil
}
