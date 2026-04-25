package db

// postgres_memory_write.go — memory node write operations.
// Covers: delete, update content, update full (props+embedding), delete-all.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

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
