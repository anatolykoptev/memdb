package db

// postgres_memory.go — memory node CRUD operations.
// Covers: Get, List, Insert, Update, Delete, Cleanup for memory nodes in the AGE graph.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// MemNode is a lightweight struct for id+memory text retrieval.
type MemNode struct {
	ID   string
	Text string
}

// MemoryInsertNode holds the data for inserting a single memory node.
type MemoryInsertNode struct {
	ID             string // properties->>'id' (UUID string)
	PropertiesJSON []byte // marshaled JSONB
	EmbeddingVec   string // "[0.1,0.2,...]" for pgvector cast
}

// GetMemoryByPropertyID retrieves a single memory node by its property UUID.
// Returns nil if not found. Used by GET /product/get_memory/{memory_id} native handler.
func (p *Postgres) GetMemoryByPropertyID(ctx context.Context, propertyID string) (map[string]any, error) {
	q := fmt.Sprintf(queries.GetMemoryByPropertyID, graphName)
	var id, propsStr string
	err := p.pool.QueryRow(ctx, q, propertyID).Scan(&id, &propsStr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return map[string]any{
		"memory_id":  id,
		"properties": propsStr,
	}, nil
}

// GetMemoriesByPropertyIDs retrieves full memory nodes by property UUID.
// No user_name filter — UUIDs are globally unique. Used by the get_memory_by_ids HTTP handler.
// Returns same shape as GetMemoryByIDs: {"memory_id": "...", "properties": "{}"}
func (p *Postgres) GetMemoriesByPropertyIDs(ctx context.Context, ids []string) ([]map[string]any, error) {
	q := fmt.Sprintf(queries.GetMemoriesByPropertyIDs, graphName)
	rows, err := p.pool.Query(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, propsStr string
		if err := rows.Scan(&id, &propsStr); err != nil {
			return nil, err
		}
		results = append(results, map[string]any{
			"memory_id":  id,
			"properties": propsStr,
		})
	}
	return results, rows.Err()
}

// GetMemoryByPropertyIDs retrieves memory nodes by their property UUID (properties->>'id').
// Used by the mem_feedback and mem_read handlers to fetch texts for LLM analysis.
// userName is required to prevent cross-user data leakage.
func (p *Postgres) GetMemoryByPropertyIDs(ctx context.Context, ids []string, userName string) ([]MemNode, error) {
	q := fmt.Sprintf(queries.GetMemoryByPropertyIDs, graphName)
	rows, err := p.pool.Query(ctx, q, ids, userName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []MemNode
	for rows.Next() {
		var n MemNode
		if err := rows.Scan(&n.ID, &n.Text); err != nil {
			return nil, err
		}
		results = append(results, n)
	}
	return results, rows.Err()
}

// ListUsers returns distinct user_name values from activated memories.
func (p *Postgres) ListUsers(ctx context.Context) ([]string, error) {
	q := fmt.Sprintf(queries.ListUsers, graphName)
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		users = append(users, name)
	}
	return users, rows.Err()
}

// CountDistinctUsers returns the number of distinct users with activated memories.
func (p *Postgres) CountDistinctUsers(ctx context.Context) (int, error) {
	q := fmt.Sprintf(queries.CountDistinctUsers, graphName)
	var count int
	err := p.pool.QueryRow(ctx, q).Scan(&count)
	return count, err
}

// ExistUser checks whether a user has any activated memories.
func (p *Postgres) ExistUser(ctx context.Context, userName string) (bool, error) {
	q := fmt.Sprintf(queries.ExistUser, graphName)
	var exists bool
	err := p.pool.QueryRow(ctx, q, userName).Scan(&exists)
	return exists, err
}

// scanPaginatedMemoryRows scans id+propsJSON pairs from a pgx rows result set.
// Shared by GetAllMemories and GetAllMemoriesByTypes to eliminate duplicate scan loops.
func scanPaginatedMemoryRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}) ([]map[string]any, error) {
	defer rows.Close()
	var results []map[string]any
	for rows.Next() {
		var id string
		var propsJSON []byte
		if err := rows.Scan(&id, &propsJSON); err != nil {
			return nil, err
		}
		results = append(results, map[string]any{
			"memory_id":  id,
			"properties": string(propsJSON),
		})
	}
	return results, rows.Err()
}

// GetAllMemoriesByTypes returns paginated memories for a user across multiple memory types.
// Used by NativePostGetMemory to fetch text_mem (LongTermMemory + UserMemory) in one query.
func (p *Postgres) GetAllMemoriesByTypes(ctx context.Context, userName string, memoryTypes []string, page, pageSize int) ([]map[string]any, int, error) {
	offset := page * pageSize

	countQ := fmt.Sprintf(queries.CountByUserAndTypes, graphName)
	var total int
	if err := p.pool.QueryRow(ctx, countQ, userName, memoryTypes).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf(queries.GetAllMemoriesByTypes, graphName)
	rows, err := p.pool.Query(ctx, q, userName, memoryTypes, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	results, err := scanPaginatedMemoryRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return results, total, nil
}

// GetAllMemories returns paginated memories for a user filtered by memory_type.
// Returns (results, totalCount, error).
func (p *Postgres) GetAllMemories(ctx context.Context, userName, memoryType string, page, pageSize int) ([]map[string]any, int, error) {
	offset := page * pageSize

	countQ := fmt.Sprintf(queries.CountByUserAndType, graphName)
	var total int
	if err := p.pool.QueryRow(ctx, countQ, userName, memoryType).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf(queries.GetAllMemories, graphName)
	rows, err := p.pool.Query(ctx, q, userName, memoryType, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	results, err := scanPaginatedMemoryRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return results, total, nil
}

// DeleteByPropertyIDs deletes nodes matching the given property IDs and user name.
// Returns the number of rows deleted.
func (p *Postgres) DeleteByPropertyIDs(ctx context.Context, propertyIDs []string, userName string) (int64, error) {
	q := fmt.Sprintf(queries.DeleteByPropertyIDs, graphName)
	tag, err := p.pool.Exec(ctx, q, propertyIDs, userName)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GetUserNamesByMemoryIDs maps property IDs to their user_name values.
func (p *Postgres) GetUserNamesByMemoryIDs(ctx context.Context, memoryIDs []string) (map[string]string, error) {
	q := fmt.Sprintf(queries.GetUserNamesByPropertyIDs, graphName)
	rows, err := p.pool.Query(ctx, q, memoryIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var propID, userName string
		if err := rows.Scan(&propID, &userName); err != nil {
			return nil, err
		}
		result[propID] = userName
	}
	return result, rows.Err()
}

// UpdateMemoryContent updates the memory text for a given memory node.
// Returns true if a row was updated, false if the memory was not found.
func (p *Postgres) UpdateMemoryContent(ctx context.Context, memoryID, content string) (bool, error) {
	q := fmt.Sprintf(queries.UpdateMemoryContent, graphName)
	tag, err := p.pool.Exec(ctx, q, memoryID, content)
	if err != nil {
		return false, fmt.Errorf("update memory: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteAllByUser deletes all activated memories for a user.
// Returns the number of rows deleted.
func (p *Postgres) DeleteAllByUser(ctx context.Context, userName string) (int64, error) {
	q := fmt.Sprintf(queries.DeleteAllByUser, graphName)
	tag, err := p.pool.Exec(ctx, q, userName)
	if err != nil {
		return 0, fmt.Errorf("delete all by user: %w", err)
	}
	return tag.RowsAffected(), nil
}

// InsertMemoryNodes inserts multiple memory nodes in a single transaction.
// Uses pgx batch for efficiency: DELETE old + INSERT new for each node.
// Each node produces two batch operations (DELETE + INSERT) = 2*len(nodes) results to consume.
func (p *Postgres) InsertMemoryNodes(ctx context.Context, nodes []MemoryInsertNode) error {
	if len(nodes) == 0 {
		return nil
	}

	delQ := fmt.Sprintf(queries.DeleteMemoryByPropID, graphName)
	insQ := fmt.Sprintf(queries.InsertMemoryNode, graphName)

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, n := range nodes {
		batch.Queue(delQ, n.ID)
		batch.Queue(insQ, n.ID, n.PropertiesJSON, n.EmbeddingVec)
	}

	br := tx.SendBatch(ctx, batch)
	for range nodes {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return fmt.Errorf("delete before insert: %w", err)
		}
		if _, err := br.Exec(); err != nil {
			br.Close()
			return fmt.Errorf("insert node: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("close batch: %w", err)
	}

	return tx.Commit(ctx)
}

// DuplicatePair represents two semantically similar memory nodes found by vector search.
type DuplicatePair struct {
	IDa   string
	MemA  string
	IDb   string
	MemB  string
	Score float64 // cosine similarity in [0, 1]
}

// FindNearDuplicates returns pairs of activated LongTermMemory/UserMemory nodes
// for a given user whose cosine similarity is >= threshold.
// Results are ordered by similarity DESC and capped at limit.
func (p *Postgres) FindNearDuplicates(ctx context.Context, userName string, threshold float64, limit int) ([]DuplicatePair, error) {
	q := fmt.Sprintf(queries.FindNearDuplicates, graphName)
	rows, err := p.pool.Query(ctx, q, userName, threshold, limit)
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

// FindNearDuplicatesByIDs is like FindNearDuplicates but restricts the scan to
// pairs where at least one node is in the given ID set.
// Used by the mem_feedback handler for targeted reorganization.
func (p *Postgres) FindNearDuplicatesByIDs(ctx context.Context, userName string, ids []string, threshold float64, limit int) ([]DuplicatePair, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := fmt.Sprintf(queries.FindNearDuplicatesByIDs, graphName)
	rows, err := p.pool.Query(ctx, q, userName, ids, threshold, limit)
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

// WMNode is a WorkingMemory node returned by GetRecentWorkingMemory.
type WMNode struct {
	ID        string
	Text      string
	TS        int64
	Embedding []float32
}

// GetRecentWorkingMemory returns the N most-recent activated WorkingMemory nodes
// for a cube including their embeddings. Used by WorkingMemoryCache.Sync to warm
// the VSET hot-cache on server startup.
func (p *Postgres) GetRecentWorkingMemory(ctx context.Context, userName string, limit int) ([]WMNode, error) {
	q := fmt.Sprintf(queries.GetRecentWorkingMemory, graphName)
	rows, err := p.pool.Query(ctx, q, userName, limit)
	if err != nil {
		return nil, fmt.Errorf("get recent working memory: %w", err)
	}
	defer rows.Close()

	var nodes []WMNode
	for rows.Next() {
		var n WMNode
		var embText string
		if err := rows.Scan(&n.ID, &n.Text, &n.TS, &embText); err != nil {
			return nil, fmt.Errorf("get recent wm scan: %w", err)
		}
		n.Embedding = ParseVectorString(embText)
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// LTMSearchResult is one result from SearchLTMByVector.
type LTMSearchResult struct {
	ID        string
	Text      string
	Score     float64
	Embedding []float32 // node's own embedding — use this for VSET VAdd, not the query embedding
}

// SearchLTMByVector returns the top-k activated LongTermMemory/UserMemory nodes
// ordered by cosine similarity to the given query embedding.
// Used by the mem_update handler to refresh WorkingMemory with contextually relevant LTM.
// embeddingVec must be a pgvector literal, e.g. "[0.1,0.2,...]".
func (p *Postgres) SearchLTMByVector(ctx context.Context, userName, embeddingVec string, minScore float64, limit int) ([]LTMSearchResult, error) {
	q := fmt.Sprintf(queries.SearchLTMByVector, graphName)
	rows, err := p.pool.Query(ctx, q, userName, embeddingVec, minScore, limit)
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

// SoftDeleteMerged marks a memory as merged into another (status: activated → merged).
// Follows MemOS memory lifecycle: merged memories remain queryable for audit/history
// but are excluded from active retrieval (search filters status='activated').
// Args: memoryID = the loser, mergedIntoID = the winner (keep_id from LLM).
func (p *Postgres) SoftDeleteMerged(ctx context.Context, memoryID, mergedIntoID, updatedAt string) error {
	q := fmt.Sprintf(queries.SoftDeleteMerged, graphName)
	_, err := p.pool.Exec(ctx, q, memoryID, mergedIntoID, updatedAt)
	if err != nil {
		return fmt.Errorf("soft delete merged %s: %w", memoryID, err)
	}
	return nil
}

// UpdateMemoryNodeFull updates the memory text, embedding, and updated_at of an existing node.
// Used by the fine-mode add pipeline when a dedup-merge decision is "update".
func (p *Postgres) UpdateMemoryNodeFull(ctx context.Context, memoryID, newText, embeddingVec, updatedAt string) error {
	q := fmt.Sprintf(queries.UpdateMemoryNodeFull, graphName)
	_, err := p.pool.Exec(ctx, q, memoryID, newText, embeddingVec, updatedAt)
	if err != nil {
		return fmt.Errorf("update memory node: %w", err)
	}
	return nil
}

// CheckContentHashExists checks whether an activated memory with the given content_hash exists for a user.
func (p *Postgres) CheckContentHashExists(ctx context.Context, hash, userName string) (bool, error) {
	q := fmt.Sprintf(queries.CheckContentHashExists, graphName)
	var exists bool
	err := p.pool.QueryRow(ctx, q, hash, userName).Scan(&exists)
	return exists, err
}

// RawMemory is a memory node identified as a raw conversation window (fast-mode artifact).
type RawMemory struct {
	ID     string // properties->>'id' (UUID)
	Memory string // raw conversation text
}

// FindRawMemories returns activated LTM/UserMemory nodes that contain raw conversation
// window patterns. These are fast-mode artifacts that should be reprocessed through fine extraction.
func (p *Postgres) FindRawMemories(ctx context.Context, userName string, limit int) ([]RawMemory, error) {
	q := fmt.Sprintf(queries.FindRawMemories, graphName)
	memTypes := []string{"LongTermMemory", "UserMemory"}
	rows, err := p.pool.Query(ctx, q, userName, memTypes, limit)
	if err != nil {
		return nil, fmt.Errorf("find raw memories: %w", err)
	}
	defer rows.Close()

	var results []RawMemory
	for rows.Next() {
		var r RawMemory
		if err := rows.Scan(&r.ID, &r.Memory); err != nil {
			return nil, fmt.Errorf("find raw memories scan: %w", err)
		}
		if r.ID != "" && r.Memory != "" {
			results = append(results, r)
		}
	}
	return results, rows.Err()
}

// CountRawMemories returns the total count of raw conversation-window memories for a user.
func (p *Postgres) CountRawMemories(ctx context.Context, userName string) (int64, error) {
	q := fmt.Sprintf(queries.CountRawMemories, graphName)
	memTypes := []string{"LongTermMemory", "UserMemory"}
	var count int64
	err := p.pool.QueryRow(ctx, q, userName, memTypes).Scan(&count)
	return count, err
}

// SearchLTMByVectorSQL returns the SearchLTMByVector SQL template for testing.
func SearchLTMByVectorSQL() string { return queries.SearchLTMByVector }

// FindNearDuplicatesSQL returns the FindNearDuplicates SQL template for testing.
func FindNearDuplicatesSQL() string { return queries.FindNearDuplicates }

// CountWorkingMemory returns the number of activated WorkingMemory nodes for a cube.
func (p *Postgres) CountWorkingMemory(ctx context.Context, userName string) (int64, error) {
	q := fmt.Sprintf(queries.CountWorkingMemory, graphName)
	var count int64
	err := p.pool.QueryRow(ctx, q, userName).Scan(&count)
	return count, err
}

// GetWorkingMemoryOldestFirst returns activated WorkingMemory nodes ordered oldest-first.
// Used by WM compaction to identify which nodes to summarize vs keep.
func (p *Postgres) GetWorkingMemoryOldestFirst(ctx context.Context, userName string, limit int) ([]MemNode, error) {
	q := fmt.Sprintf(queries.GetWorkingMemoryOldestFirst, graphName)
	rows, err := p.pool.Query(ctx, q, userName, limit)
	if err != nil {
		return nil, fmt.Errorf("get wm oldest first: %w", err)
	}
	defer rows.Close()

	var nodes []MemNode
	for rows.Next() {
		var n MemNode
		if err := rows.Scan(&n.ID, &n.Text); err != nil {
			return nil, fmt.Errorf("get wm oldest first scan: %w", err)
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// CleanupWorkingMemory deletes oldest WorkingMemory nodes beyond keepLatest for a user.
// Returns the number of rows deleted.
func (p *Postgres) CleanupWorkingMemory(ctx context.Context, userName string, keepLatest int) (int64, error) {
	q := fmt.Sprintf(queries.CleanupOldestWorkingMemory, graphName)
	tag, err := p.pool.Exec(ctx, q, userName, keepLatest)
	if err != nil {
		return 0, fmt.Errorf("cleanup working memory: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CleanupWorkingMemoryWithIDs deletes oldest WorkingMemory nodes beyond keepLatest
// and returns the node UUIDs of deleted rows for VSET cache eviction.
func (p *Postgres) CleanupWorkingMemoryWithIDs(ctx context.Context, userName string, keepLatest int) ([]string, error) {
	q := fmt.Sprintf(queries.CleanupOldestWorkingMemoryReturning, graphName)
	rows, err := p.pool.Query(ctx, q, userName, keepLatest)
	if err != nil {
		return nil, fmt.Errorf("cleanup working memory with ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// UpdateMemoryByID atomically replaces properties + embedding for a memory
// node scoped by (memory_id, cube_id). Returns an error wrapping "memory not
// found" when the row does not exist or the cube_id does not match.
func (p *Postgres) UpdateMemoryByID(ctx context.Context, memoryID, cubeID string, propsJSON []byte, embedding string) error {
	q := fmt.Sprintf(queries.UpdateMemoryPropsAndEmbedding, graphName)
	var returnedID string
	err := p.pool.QueryRow(ctx, q, memoryID, cubeID, propsJSON, embedding).Scan(&returnedID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("memory not found: id=%s cube=%s", memoryID, cubeID)
		}
		return fmt.Errorf("update memory: %w", err)
	}
	return nil
}

// ListCubesByTag returns distinct cube IDs whose activated memories include
// the given tag in their properties->'tags' array.
func (p *Postgres) ListCubesByTag(ctx context.Context, tag string) ([]string, error) {
	q := fmt.Sprintf(queries.ListCubesByTag, graphName)
	rows, err := p.pool.Query(ctx, q, tag)
	if err != nil {
		return nil, fmt.Errorf("list cubes by tag: %w", err)
	}
	defer rows.Close()
	var cubes []string
	for rows.Next() {
		var cube string
		if err := rows.Scan(&cube); err != nil {
			return nil, err
		}
		cubes = append(cubes, cube)
	}
	return cubes, rows.Err()
}
