package scheduler

// reorganizer_mem_read.go — mem_read handler: raw WorkingMemory → LTM pipeline.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
)

// enhancementFact is one structured memory extracted by the LLM from a raw WM note.
type enhancementFact struct {
	Text string `json:"text"`
	Type string `json:"type"` // "LongTermMemory" or "UserMemory"
}

// ProcessRawMemory implements the Go-native mem_read handler.
//
// Python's scheduler sends raw WorkingMemory IDs for LLM "fine transfer":
//  1. Fetch WM node texts from Postgres
//  2. For each WM node call LLM to extract structured LTM facts
//  3. Embed each fact and insert as LTM nodes
//  4. Delete the original WM nodes (and evict from VSET)
//
// Mirrors Python's _process_memories_with_reader / fine_transfer_simple_mem.
// Non-fatal: errors per-node are logged; the node is left in Postgres as-is.
func (r *Reorganizer) ProcessRawMemory(ctx context.Context, cubeID string, wmIDs []string) {
	if len(wmIDs) == 0 {
		return
	}
	if r.embedder == nil {
		r.logger.Debug("mem_read: embedder not configured, skipping")
		return
	}

	log := r.logger.With(slog.String("cube_id", cubeID), slog.Int("wm_ids", len(wmIDs)))
	log.Info("mem_read: processing raw WM nodes")

	// Step 1: fetch WM node content by property UUID.
	nodes, err := r.postgres.GetMemoryByPropertyIDs(ctx, wmIDs, cubeID)
	if err != nil || len(nodes) == 0 {
		log.Warn("mem_read: GetMemoryByPropertyIDs failed or returned empty", slog.Any("error", err))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	var inserted, skipped int

	for _, node := range nodes {
		rawText := node.Text
		wmID := node.ID
		if rawText == "" || wmID == "" {
			skipped++
			continue
		}

		// Step 2: LLM enhancement.
		facts, err := r.llmEnhance(ctx, rawText)
		if err != nil {
			log.Warn("mem_read: llmEnhance failed", slog.String("wm_id", wmID), slog.Any("error", err))
			skipped++
			continue
		}
		if len(facts) == 0 {
			log.Debug("mem_read: no facts extracted", slog.String("wm_id", wmID))
			r.deleteWMNode(ctx, cubeID, wmID, log)
			continue
		}

		// Step 3 & 4: embed + insert LTM nodes.
		texts := make([]string, len(facts))
		for i, f := range facts {
			texts[i] = f.Text
		}
		embs, err := r.embedder.Embed(ctx, texts)
		if err != nil {
			log.Warn("mem_read: embed failed", slog.String("wm_id", wmID), slog.Any("error", err))
			skipped++
			continue
		}

		var ltmNodes []db.MemoryInsertNode
		for i, f := range facts {
			if i >= len(embs) || len(embs[i]) == 0 {
				continue
			}
			memType := f.Type
			if memType != "UserMemory" {
				memType = "LongTermMemory"
			}
			ltID := uuid.New().String()
			props := map[string]any{
				"id":               ltID,
				"memory":           f.Text,
				"memory_type":      memType,
				"user_name":        cubeID,
				"user_id":          cubeID,
				"status":           "activated",
				"created_at":       now,
				"updated_at":       now,
				"tags":             []string{"mode:mem_read"},
				"background":       "working_binding:" + wmID,
				"delete_time":      "",
				"delete_record_id": "",
			}
			propsJSON, _ := json.Marshal(props)
			ltmNodes = append(ltmNodes, db.MemoryInsertNode{
				ID:             ltID,
				PropertiesJSON: propsJSON,
				EmbeddingVec:   db.FormatVector(embs[i]),
			})
		}
		if len(ltmNodes) > 0 {
			if err := r.postgres.InsertMemoryNodes(ctx, ltmNodes); err != nil {
				log.Warn("mem_read: InsertMemoryNodes failed", slog.String("wm_id", wmID), slog.Any("error", err))
				skipped++
				continue
			}
			// Record EXTRACTED_FROM edges: each LTM node was extracted from this WM node.
			for _, ltmNode := range ltmNodes {
				if err := r.postgres.CreateMemoryEdge(ctx, ltmNode.ID, wmID, db.EdgeExtractedFrom, now, ""); err != nil {
					log.Debug("mem_read: create extracted_from edge failed (non-fatal)",
						slog.String("ltm_id", ltmNode.ID), slog.String("wm_id", wmID), slog.Any("error", err))
				}
			}
			inserted += len(ltmNodes)
		}

		// Step 5: delete raw WM node.
		r.deleteWMNode(ctx, cubeID, wmID, log)
	}

	log.Info("mem_read: complete",
		slog.Int("wm_nodes_processed", len(nodes)),
		slog.Int("ltm_inserted", inserted),
		slog.Int("skipped", skipped),
	)
}

// llmEnhance calls the LLM to extract structured facts from a raw WM note.
func (r *Reorganizer) llmEnhance(ctx context.Context, rawText string) ([]enhancementFact, error) {
	msgs := []map[string]string{
		{"role": "system", "content": memEnhancementSystemPrompt},
		{"role": "user", "content": "Raw working memory note:\n" + rawText},
	}

	callCtx, cancel := context.WithTimeout(ctx, reorganizerLLMTimeout)
	defer cancel()

	raw, err := r.callLLM(callCtx, msgs, 512)
	if err != nil {
		return nil, err
	}

	raw = stripFences(raw)
	var result struct {
		Memories []enhancementFact `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("parse llm enhance json (%s): %w", truncate(raw, 200), err)
	}
	return result.Memories, nil
}

// deleteWMNode deletes a WorkingMemory node from Postgres and evicts from VSET.
func (r *Reorganizer) deleteWMNode(ctx context.Context, cubeID, wmID string, log *slog.Logger) {
	if _, err := r.postgres.DeleteByPropertyIDs(ctx, []string{wmID}, cubeID); err != nil {
		log.Debug("mem_read: delete WM node failed (non-fatal)", slog.String("wm_id", wmID), slog.Any("error", err))
	}
	if r.wmCache != nil {
		if err := r.wmCache.VRem(ctx, cubeID, wmID); err != nil {
			log.Debug("mem_read: vset evict failed (non-fatal)", slog.String("wm_id", wmID), slog.Any("error", err))
		}
	}
}
