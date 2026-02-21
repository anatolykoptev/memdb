package scheduler

// reorganizer_wm.go — mem_update handler: WorkingMemory hot-cache refresh.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
)

func (r *Reorganizer) generateUUID() string {
	return uuid.New().String()
}

func (r *Reorganizer) buildWMProps(id, text, cubeID, now, background string) []byte {
	props := map[string]any{
		"id":                id,
		"memory":            text,
		"memory_type":       "WorkingMemory",
		"status":            "activated",
		"user_name":         cubeID,
		"user_id":           cubeID,
		"session_id":        "",
		"created_at":        now,
		"updated_at":        now,
		"delete_time":       "",
		"delete_record_id":  "",
		"tags":              []string{"mode:query"},
		"key":               "",
		"usage":             []string{},
		"sources":           []string{},
		"background":        background,
		"confidence":        0.99,
		"type":              "fact",
		"info":              map[string]any{},
		"graph_id":          uuid.New().String(),
		"importance_score":  1.0,
		"retrieval_count":   0,
		"last_retrieved_at": "",
	}
	b, _ := json.Marshal(props)
	return b
}

// RefreshWorkingMemory implements the Go-native mem_update handler.
//
// When a user sends a query, Python's scheduler publishes a "mem_update" message
// containing the query text. This method mirrors Python's process_session_turn:
//  1. Embed the query using the configured embedder
//  2. Search Postgres LTM for the top-k nodes most similar to the query
//  3. Add each result to the VSET hot cache (VAdd) — they become WM candidates
//     for the next search/chat request
//
// Non-fatal: embedding or DB errors are logged and the method returns without
// error so the worker always XACKs the message.
func (r *Reorganizer) RefreshWorkingMemory(ctx context.Context, cubeID, queryText string) {
	log := r.logger.With(
		slog.String("cube_id", cubeID),
		slog.String("query_preview", truncate(queryText, 60)),
	)

	if r.embedder == nil {
		log.Debug("wm refresh: embedder not configured, skipping")
		return
	}
	if r.wmCache == nil {
		log.Debug("wm refresh: wmCache not configured, skipping")
		return
	}
	if queryText == "" {
		log.Debug("wm refresh: empty query, skipping")
		return
	}

	// Step 1: embed the query with a short deadline.
	embedCtx, cancel := context.WithTimeout(ctx, wmRefreshEmbedTimeout)
	defer cancel()

	embs, err := r.embedder.Embed(embedCtx, []string{queryText})
	if err != nil || len(embs) == 0 || len(embs[0]) == 0 {
		log.Warn("wm refresh: embedding failed", slog.Any("error", err))
		return
	}
	queryVec := embs[0]
	queryVecStr := db.FormatVector(queryVec)

	// Step 2: search LTM for top-k similar memories.
	results, err := r.postgres.SearchLTMByVector(ctx, cubeID, queryVecStr, wmRefreshMinScore, wmRefreshTopK)
	if err != nil {
		log.Warn("wm refresh: SearchLTMByVector failed", slog.Any("error", err))
		return
	}
	if len(results) == 0 {
		log.Debug("wm refresh: no relevant LTM found")
		return
	}
	log.Debug("wm refresh: found LTM candidates", slog.Int("count", len(results)))

	// Step 3: Insert new WorkingMemory nodes to Postgres and add them to VSET hot cache.
	// VAdd is idempotent (CAS flag) — already-present nodes are skipped.
	// Use the LTM node's own embedding (not the query vec) so that future VSim
	// calls correctly compare user queries against memory content.
	nowStr := time.Now().UTC().Format("2006-01-02T15:04:05.000000")
	nowUnix := time.Now().UTC().Unix()
	
	var allNodes []db.MemoryInsertNode
	var vsetInserts []struct{ id, text string; emb []float32 }
	
	for _, res := range results {
		emb := res.Embedding
		if len(emb) == 0 {
			emb = queryVec
		}
		
		wmID := r.generateUUID()
		propsJSON := r.buildWMProps(wmID, res.Text, cubeID, nowStr, "[working_binding:"+res.ID+"]")
		embStr := db.FormatVector(emb)

		allNodes = append(allNodes, db.MemoryInsertNode{
			ID: wmID, PropertiesJSON: propsJSON, EmbeddingVec: embStr,
		})
		vsetInserts = append(vsetInserts, struct{ id, text string; emb []float32 }{
			id: wmID, text: res.Text, emb: emb,
		})
	}
	
	if len(allNodes) > 0 {
		if err := r.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
			log.Warn("wm refresh: InsertMemoryNodes failed", slog.Any("error", err))
		} else {
			added := 0
			for _, vi := range vsetInserts {
				if err := r.wmCache.VAdd(ctx, cubeID, vi.id, vi.text, vi.emb, nowUnix); err != nil {
					log.Debug("wm refresh: vset vadd failed",
						slog.String("id", vi.id), slog.Any("error", err))
					continue
				}
				added++
			}
			log.Info("wm refresh: complete",
				slog.Int("candidates", len(results)),
				slog.Int("added_to_vset", added),
			)
		}
	} else {
		log.Info("wm refresh: no LTM found (or failed to build nodes)")
	}
}
