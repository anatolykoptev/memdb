package scheduler

// reorganizer_mem_read.go — mem_read handler: raw WorkingMemory → LTM pipeline.
//
// When llmExtractor is available, runs full fine-level processing:
//   1. Fetch WM nodes with full properties
//   2. Concatenate into conversation block
//   3. Fetch dedup candidates (VSET + pgvector)
//   4. One LLM call: ExtractAndDedup(conversation, candidates)
//   5. Content-hash dedup for ADD facts
//   6. Batch embed ADD/UPDATE facts
//   7. Apply actions: ADD → insert WM+LTM, UPDATE → merge, DELETE → invalidate+remove
//   8. Entity linking (async goroutine)
//   9. Delete original WM staging nodes
//  10. Episodic summary (async goroutine)
//  11. Profiler TriggerRefresh (async)
//
// Falls back to the old llmEnhance path when llmExtractor is nil.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/llm"
)

// enhancementFact is one structured memory extracted by the LLM from a raw WM note (legacy path).
type enhancementFact struct {
	Text string `json:"text"`
	Type string `json:"type"` // "LongTermMemory" or "UserMemory"
}

// ProcessRawMemory implements the Go-native mem_read handler.
//
// When llmExtractor is available, runs the full fine-level pipeline (ExtractAndDedup,
// content-hash dedup, entity linking, episodic summary, profiler refresh).
// Falls back to the legacy llmEnhance path when llmExtractor is nil.
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

	// Guard: use fine-level pipeline only if llmExtractor is available.
	if r.llmExtractor != nil {
		r.processRawMemoryFine(ctx, cubeID, wmIDs, log)
		return
	}

	// Legacy path: simple llmEnhance per node.
	r.processRawMemoryLegacy(ctx, cubeID, wmIDs, log)
}

// processRawMemoryFine runs the full fine-level pipeline for async mem_read.
func (r *Reorganizer) processRawMemoryFine(ctx context.Context, cubeID string, wmIDs []string, log *slog.Logger) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000")

	// Step 1: fetch WM nodes with full properties.
	fullNodes, err := r.postgres.GetMemoriesByPropertyIDs(ctx, wmIDs)
	if err != nil || len(fullNodes) == 0 {
		log.Warn("mem_read: GetMemoriesByPropertyIDs failed or returned empty",
			slog.Any("error", err), slog.Int("results", len(fullNodes)))
		return
	}

	// Extract texts, session_id, agent_id from WM properties.
	var texts []string
	var sessionID, agentID string
	var processedWMIDs []string
	for _, fn := range fullNodes {
		propsStr, _ := fn["properties"].(string)
		if propsStr == "" {
			continue
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(propsStr), &props); err != nil {
			continue
		}
		mem, _ := props["memory"].(string)
		id, _ := props["id"].(string)
		if mem == "" || id == "" {
			continue
		}
		texts = append(texts, mem)
		processedWMIDs = append(processedWMIDs, id)
		if sessionID == "" {
			sessionID, _ = props["session_id"].(string)
		}
		if agentID == "" {
			agentID, _ = props["agent_id"].(string)
		}
	}
	if len(texts) == 0 {
		log.Debug("mem_read: no valid WM texts found")
		return
	}

	// Step 2: concatenate WM texts into conversation block.
	conversation := strings.Join(texts, "\n")

	// Step 3: fetch dedup candidates (two-tier: VSET + pgvector).
	candidates := r.fetchMemReadCandidates(ctx, conversation, cubeID, agentID, log)

	// Step 4: one LLM call — ExtractAndDedup.
	facts, err := r.llmExtractor.ExtractAndDedup(ctx, conversation, candidates)
	if err != nil {
		log.Warn("mem_read: ExtractAndDedup failed", slog.Any("error", err))
		return
	}
	if len(facts) == 0 {
		log.Debug("mem_read: no facts extracted")
		// Still delete WM staging nodes.
		r.deleteWMNodes(ctx, cubeID, processedWMIDs, log)
		return
	}
	log.Info("mem_read: extracted facts",
		slog.Int("count", len(facts)),
		slog.String("model", r.llmExtractor.Model()),
	)

	// Step 5: content-hash dedup for ADD facts.
	facts = r.filterAddsByContentHash(ctx, facts, cubeID, log)

	// Step 6: batch embed all ADD/UPDATE facts.
	embedded := r.embedFacts(ctx, facts, log)

	// Step 7: apply actions.
	var allNodes []db.MemoryInsertNode
	var inserted, updated, deleted int
	for i := range embedded {
		ef := &embedded[i]
		f := ef.fact
		switch f.Action {
		case llm.MemSkip:
			continue

		case llm.MemDelete:
			if f.TargetID != "" {
				if err := r.postgres.InvalidateEdgesByMemoryID(ctx, f.TargetID, now); err != nil {
					log.Debug("mem_read: invalidate edges failed", slog.String("id", f.TargetID), slog.Any("error", err))
				}
				if err := r.postgres.InvalidateEntityEdgesByMemoryID(ctx, f.TargetID, now); err != nil {
					log.Debug("mem_read: invalidate entity edges failed", slog.String("id", f.TargetID), slog.Any("error", err))
				}
				if _, err := r.postgres.DeleteByPropertyIDs(ctx, []string{f.TargetID}, cubeID); err != nil {
					log.Debug("mem_read: delete contradicted memory failed", slog.String("id", f.TargetID), slog.Any("error", err))
				}
				if r.wmCache != nil {
					_ = r.wmCache.VRem(ctx, cubeID, f.TargetID)
				}
				deleted++
			}

		case llm.MemUpdate:
			if f.TargetID != "" && ef.embVec != "" {
				if err := r.postgres.InvalidateEdgesByMemoryID(ctx, f.TargetID, now); err != nil {
					log.Debug("mem_read: invalidate edges on update failed", slog.String("id", f.TargetID), slog.Any("error", err))
				}
				if err := r.postgres.InvalidateEntityEdgesByMemoryID(ctx, f.TargetID, now); err != nil {
					log.Debug("mem_read: invalidate entity edges on update failed", slog.String("id", f.TargetID), slog.Any("error", err))
				}
				if err := r.postgres.UpdateMemoryNodeFull(ctx, f.TargetID, f.Memory, ef.embVec, now); err != nil {
					log.Debug("mem_read: update node failed", slog.String("id", f.TargetID), slog.Any("error", err))
				} else {
					updated++
				}
				ef.ltmID = f.TargetID
			}

		default: // llm.MemAdd
			if ef.embVec == "" {
				continue
			}
			createdAt := now
			if f.ValidAt != "" {
				createdAt = f.ValidAt
			}
			memType := f.Type
			if memType != "UserMemory" {
				memType = "LongTermMemory"
			}
			ltID := uuid.New().String()
			allTags := append([]string{"mode:mem_read"}, f.Tags...)

			factInfo := map[string]any{}
			if f.ContentHash != "" {
				factInfo["content_hash"] = f.ContentHash
			}
			if f.Confidence > 0 {
				factInfo["confidence"] = f.Confidence
			}
			if f.ValidAt != "" {
				factInfo["valid_at"] = f.ValidAt
			}

			props := map[string]any{
				"id":               ltID,
				"memory":           f.Memory,
				"memory_type":      memType,
				"user_name":        cubeID,
				"user_id":          cubeID,
				"agent_id":         agentID,
				"session_id":       sessionID,
				"status":           "activated",
				"created_at":       createdAt,
				"updated_at":       now,
				"tags":             allTags,
				"background":       "",
				"delete_time":      "",
				"delete_record_id": "",
				"confidence":       f.Confidence,
				"type":             "fact",
				"info":             factInfo,
				"graph_id":         uuid.New().String(),
				"importance_score": 1.0,
				"retrieval_count":  0,
			}
			propsJSON, err := json.Marshal(props)
			if err != nil {
				continue
			}
			allNodes = append(allNodes, db.MemoryInsertNode{
				ID:             ltID,
				PropertiesJSON: propsJSON,
				EmbeddingVec:   ef.embVec,
			})
			ef.ltmID = ltID
			inserted++
		}
	}

	// Step 8: batch insert all ADD nodes.
	if len(allNodes) > 0 {
		if err := r.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
			log.Warn("mem_read: InsertMemoryNodes failed", slog.Any("error", err))
		} else {
			// Create EXTRACTED_FROM edges for all new LTM nodes.
			for _, ltmNode := range allNodes {
				for _, wmID := range processedWMIDs {
					if err := r.postgres.CreateMemoryEdge(ctx, ltmNode.ID, wmID, db.EdgeExtractedFrom, now, ""); err != nil {
						log.Debug("mem_read: create extracted_from edge failed (non-fatal)",
							slog.String("ltm_id", ltmNode.ID), slog.String("wm_id", wmID), slog.Any("error", err))
					}
				}
			}
		}
	}

	// Step 9: entity linking (async goroutine).
	r.linkEntities(embedded, cubeID, now)

	// Step 10: delete original WM staging nodes.
	r.deleteWMNodes(ctx, cubeID, processedWMIDs, log)

	// Step 11: episodic summary (async) — if session_id present.
	if sessionID != "" {
		r.generateEpisodicSummary(cubeID, sessionID, conversation, now)
	}

	// Step 12: profiler TriggerRefresh (async).
	if r.profiler != nil {
		r.profiler.TriggerRefresh(cubeID)
	}

	log.Info("mem_read: complete",
		slog.Int("wm_nodes_processed", len(processedWMIDs)),
		slog.Int("ltm_inserted", inserted),
		slog.Int("updated", updated),
		slog.Int("deleted", deleted),
	)
}

// fetchMemReadCandidates fetches dedup candidates for the mem_read pipeline (two-tier).
func (r *Reorganizer) fetchMemReadCandidates(ctx context.Context, conversation, cubeID, agentID string, log *slog.Logger) []llm.Candidate {
	const candidateLimit = 10
	head := conversation
	if len(head) > 512 {
		head = head[:512]
	}
	convEmbs, err := r.embedder.Embed(ctx, []string{head})
	if err != nil || len(convEmbs) == 0 || len(convEmbs[0]) == 0 {
		log.Debug("mem_read: embed for candidates failed", slog.Any("error", err))
		return nil
	}
	embedding := convEmbs[0]

	seen := make(map[string]struct{})
	out := make([]llm.Candidate, 0, candidateLimit)

	// Tier 1: VSET hot cache.
	if r.wmCache != nil {
		vsetResults, err := r.wmCache.VSim(ctx, cubeID, embedding, candidateLimit)
		if err != nil {
			log.Debug("mem_read: vset vsim failed", slog.Any("error", err))
		} else {
			for _, vr := range vsetResults {
				if vr.ID != "" && vr.Memory != "" {
					out = append(out, llm.Candidate{ID: vr.ID, Memory: vr.Memory})
					seen[vr.ID] = struct{}{}
				}
			}
		}
	}

	// Tier 2: Postgres pgvector.
	results, err := r.postgres.VectorSearch(ctx, embedding, cubeID,
		[]string{"LongTermMemory", "UserMemory"}, agentID, candidateLimit)
	if err != nil {
		log.Debug("mem_read: postgres vector search failed", slog.Any("error", err))
	} else {
		for _, vr := range results {
			id, mem := extractIDAndMemory(vr.Properties)
			if id == "" || mem == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			out = append(out, llm.Candidate{ID: id, Memory: mem})
			seen[id] = struct{}{}
		}
	}

	return out
}

// filterAddsByContentHash removes ADD facts whose content_hash already exists in the DB.
func (r *Reorganizer) filterAddsByContentHash(ctx context.Context, facts []llm.ExtractedFact, cubeID string, log *slog.Logger) []llm.ExtractedFact {
	type entry struct {
		idx  int
		hash string
	}
	var addEntries []entry
	hashes := make([]string, 0, len(facts))
	for i, f := range facts {
		if f.Action != llm.MemAdd {
			continue
		}
		if f.Memory == "" {
			continue
		}
		h := memReadTextHash(f.Memory)
		addEntries = append(addEntries, entry{idx: i, hash: h})
		hashes = append(hashes, h)
	}
	if len(hashes) == 0 {
		return facts
	}

	existing, err := r.postgres.FilterExistingContentHashes(ctx, hashes, cubeID)
	if err != nil {
		log.Debug("mem_read: batch hash check failed (skipping hash dedup)", slog.Any("error", err))
		return facts
	}

	skipped := 0
	for _, e := range addEntries {
		if existing[e.hash] {
			facts[e.idx].Action = llm.MemSkip
			skipped++
		} else if facts[e.idx].ContentHash == "" {
			facts[e.idx].ContentHash = e.hash
		}
	}
	if skipped > 0 {
		log.Debug("mem_read: skipped exact duplicates by content_hash", slog.Int("skipped", skipped))
	}
	return facts
}

// embeddedMemReadFact pairs an ExtractedFact with its embedding and assigned LTM ID.
type embeddedMemReadFact struct {
	fact      llm.ExtractedFact
	embedding []float32
	embVec    string
	ltmID     string
}

// embedFacts embeds all ADD/UPDATE facts in a single batched ONNX inference call.
func (r *Reorganizer) embedFacts(ctx context.Context, facts []llm.ExtractedFact, log *slog.Logger) []embeddedMemReadFact {
	out := make([]embeddedMemReadFact, len(facts))
	for i, f := range facts {
		out[i].fact = f
	}

	indices := make([]int, 0, len(facts))
	embTexts := make([]string, 0, len(facts))
	for i, f := range facts {
		if f.Action == llm.MemDelete || f.Action == llm.MemSkip || f.Memory == "" {
			continue
		}
		indices = append(indices, i)
		embTexts = append(embTexts, f.Memory)
	}
	if len(embTexts) == 0 {
		return out
	}

	embs, err := r.embedder.Embed(ctx, embTexts)
	if err != nil {
		log.Debug("mem_read: batch embed failed", slog.Any("error", err))
		return out
	}

	for j, idx := range indices {
		if j >= len(embs) || len(embs[j]) == 0 {
			continue
		}
		out[idx].embedding = embs[j]
		out[idx].embVec = db.FormatVector(embs[j])
	}
	return out
}

// extractIDAndMemory parses a properties JSON blob to extract the id and memory fields.
func extractIDAndMemory(propertiesJSON string) (id, memory string) {
	var props map[string]any
	if err := json.Unmarshal([]byte(propertiesJSON), &props); err != nil {
		return "", ""
	}
	id, _ = props["id"].(string)
	memory, _ = props["memory"].(string)
	return id, memory
}

// deleteWMNodes deletes multiple WorkingMemory nodes from Postgres and evicts from VSET.
func (r *Reorganizer) deleteWMNodes(ctx context.Context, cubeID string, wmIDs []string, log *slog.Logger) {
	for _, wmID := range wmIDs {
		r.deleteWMNode(ctx, cubeID, wmID, log)
	}
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

// memReadTextHash computes a 16-byte SHA-256 content hash of the normalized text.
func memReadTextHash(text string) string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:16])
}

// --- Legacy path (fallback when llmExtractor is nil) ---

// processRawMemoryLegacy is the old simple pipeline: llmEnhance → embed → insert.
func (r *Reorganizer) processRawMemoryLegacy(ctx context.Context, cubeID string, wmIDs []string, log *slog.Logger) {
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

		legacyTexts := make([]string, len(facts))
		for i, f := range facts {
			legacyTexts[i] = f.Text
		}
		embs, err := r.embedder.Embed(ctx, legacyTexts)
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
			for _, ltmNode := range ltmNodes {
				if err := r.postgres.CreateMemoryEdge(ctx, ltmNode.ID, wmID, db.EdgeExtractedFrom, now, ""); err != nil {
					log.Debug("mem_read: create extracted_from edge failed (non-fatal)",
						slog.String("ltm_id", ltmNode.ID), slog.String("wm_id", wmID), slog.Any("error", err))
				}
			}
			inserted += len(ltmNodes)
		}

		r.deleteWMNode(ctx, cubeID, wmID, log)
	}

	log.Info("mem_read: complete",
		slog.Int("wm_nodes_processed", len(nodes)),
		slog.Int("ltm_inserted", inserted),
		slog.Int("skipped", skipped),
	)
}

// llmEnhance calls the LLM to extract structured facts from a raw WM note (legacy path).
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
