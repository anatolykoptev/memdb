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

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// memReadCandidateHeadChars is the character limit for the conversation head
// when embedding for dedup candidate lookup.
const memReadCandidateHeadChars = 512

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
func (r *Reorganizer) ProcessRawMemory(ctx context.Context, userID, cubeID string, wmIDs []string) {
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
		r.processRawMemoryFine(ctx, userID, cubeID, wmIDs, log)
		return
	}

	// Legacy path: simple llmEnhance per node.
	r.processRawMemoryLegacy(ctx, userID, cubeID, wmIDs, log)
}

// wmInfo holds the extracted fields from WorkingMemory node properties.
type wmInfo struct {
	texts          []string
	sessionID      string
	agentID        string
	processedWMIDs []string
}

// actionCounts holds outcome counters for Step 7.
type actionCounts struct{ inserted, updated, deleted int }

// processRawMemoryFine runs the full fine-level pipeline for async mem_read.
func (r *Reorganizer) processRawMemoryFine(ctx context.Context, userID, cubeID string, wmIDs []string, log *slog.Logger) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000")

	fullNodes, err := r.postgres.GetMemoriesByPropertyIDs(ctx, wmIDs)
	if err != nil || len(fullNodes) == 0 {
		log.Warn("mem_read: GetMemoriesByPropertyIDs failed or returned empty",
			slog.Any("error", err), slog.Int("results", len(fullNodes)))
		return
	}

	info := extractWMInfo(fullNodes)
	if len(info.texts) == 0 {
		log.Debug("mem_read: no valid WM texts found")
		return
	}
	conversation := strings.Join(info.texts, "\n")

	candidates := r.fetchMemReadCandidates(ctx, conversation, cubeID, info.agentID, log)
	facts, err := r.llmExtractor.ExtractAndDedup(ctx, conversation, candidates)
	if err != nil {
		log.Warn("mem_read: ExtractAndDedup failed", slog.Any("error", err))
		return
	}
	if len(facts) == 0 {
		log.Debug("mem_read: no facts extracted")
		r.deleteWMNodes(ctx, cubeID, info.processedWMIDs, log)
		return
	}
	log.Info("mem_read: extracted facts", slog.Int("count", len(facts)), slog.String("model", r.llmExtractor.Model()))

	facts = r.filterAddsByContentHash(ctx, facts, cubeID, log)
	embedded := r.embedFacts(ctx, facts, log)

	allNodes, counts := r.applyMemoryActions(ctx, embedded, userID, cubeID, info.agentID, info.sessionID, now, log)
	r.insertAndLinkLTMNodes(ctx, allNodes, info.processedWMIDs, now, log)
	r.linkEntities(embedded, cubeID, now)
	r.deleteWMNodes(ctx, cubeID, info.processedWMIDs, log)

	if info.sessionID != "" {
		r.generateEpisodicSummary(userID, cubeID, info.sessionID, conversation, now)
	}
	if r.profiler != nil {
		r.profiler.TriggerRefresh(cubeID)
	}

	log.Info("mem_read: complete",
		slog.Int("wm_nodes_processed", len(info.processedWMIDs)),
		slog.Int("ltm_inserted", counts.inserted),
		slog.Int("updated", counts.updated),
		slog.Int("deleted", counts.deleted),
	)
}

// extractWMInfo extracts texts, sessionID, agentID and property IDs from raw WM node rows.
func extractWMInfo(fullNodes []map[string]any) wmInfo {
	var info wmInfo
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
		info.texts = append(info.texts, mem)
		info.processedWMIDs = append(info.processedWMIDs, id)
		if info.sessionID == "" {
			info.sessionID, _ = props["session_id"].(string)
		}
		if info.agentID == "" {
			info.agentID, _ = props["agent_id"].(string)
		}
	}
	return info
}

// applyMemoryActions applies ADD/UPDATE/DELETE actions from the embedded facts.
// Returns new LTM nodes to insert and outcome counters.
func (r *Reorganizer) applyMemoryActions(
	ctx context.Context,
	embedded []embeddedMemReadFact,
	userID, cubeID, agentID, sessionID, now string,
	log *slog.Logger,
) ([]db.MemoryInsertNode, actionCounts) {
	var allNodes []db.MemoryInsertNode
	var counts actionCounts
	for i := range embedded {
		ef := &embedded[i]
		switch ef.fact.Action {
		case llm.MemSkip:
			continue
		case llm.MemDelete:
			if r.applyMemDelete(ctx, ef.fact.TargetID, cubeID, now, log) {
				counts.deleted++
			}
		case llm.MemUpdate:
			if r.applyMemUpdate(ctx, ef, now, log) {
				counts.updated++
			}
		default: // llm.MemAdd
			node, ltmID, ok := buildLTMNode(ef, userID, cubeID, agentID, sessionID, now)
			if ok {
				allNodes = append(allNodes, node)
				ef.ltmID = ltmID
				counts.inserted++
			}
		}
	}
	return allNodes, counts
}

// applyMemDelete invalidates edges and deletes a memory node. Returns true if deleted.
func (r *Reorganizer) applyMemDelete(ctx context.Context, targetID, cubeID, now string, log *slog.Logger) bool {
	if targetID == "" {
		return false
	}
	if err := r.postgres.InvalidateEdgesByMemoryID(ctx, targetID, now); err != nil {
		log.Debug("mem_read: invalidate edges failed", slog.String("id", targetID), slog.Any("error", err))
	}
	if err := r.postgres.InvalidateEntityEdgesByMemoryID(ctx, targetID, now); err != nil {
		log.Debug("mem_read: invalidate entity edges failed", slog.String("id", targetID), slog.Any("error", err))
	}
	if _, err := r.postgres.DeleteByPropertyIDs(ctx, []string{targetID}, cubeID); err != nil {
		log.Debug("mem_read: delete contradicted memory failed", slog.String("id", targetID), slog.Any("error", err))
	}
	if r.wmCache != nil {
		_ = r.wmCache.VRem(ctx, cubeID, targetID)
	}
	return true
}

// applyMemUpdate invalidates edges and updates the memory node in place. Returns true if updated.
func (r *Reorganizer) applyMemUpdate(ctx context.Context, ef *embeddedMemReadFact, now string, log *slog.Logger) bool {
	f := ef.fact
	if f.TargetID == "" || ef.embVec == "" {
		return false
	}
	if err := r.postgres.InvalidateEdgesByMemoryID(ctx, f.TargetID, now); err != nil {
		log.Debug("mem_read: invalidate edges on update failed", slog.String("id", f.TargetID), slog.Any("error", err))
	}
	if err := r.postgres.InvalidateEntityEdgesByMemoryID(ctx, f.TargetID, now); err != nil {
		log.Debug("mem_read: invalidate entity edges on update failed", slog.String("id", f.TargetID), slog.Any("error", err))
	}
	if err := r.postgres.UpdateMemoryNodeFull(ctx, f.TargetID, f.Memory, ef.embVec, now); err != nil {
		log.Debug("mem_read: update node failed", slog.String("id", f.TargetID), slog.Any("error", err))
		return false
	}
	ef.ltmID = f.TargetID
	return true
}

// buildLTMNode constructs a MemoryInsertNode for a MemAdd fact.
// Returns (node, ltmID, ok).
func buildLTMNode(ef *embeddedMemReadFact, userID, cubeID, agentID, sessionID, now string) (db.MemoryInsertNode, string, bool) {
	f := ef.fact
	if ef.embVec == "" {
		return db.MemoryInsertNode{}, "", false
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
		"id": ltID, "memory": f.Memory, "memory_type": memType,
		"user_name": cubeID, "user_id": userID, "agent_id": agentID, "session_id": sessionID,
		"status": "activated", "created_at": createdAt, "updated_at": now,
		"tags":       append([]string{"mode:mem_read"}, f.Tags...),
		"background": "", "delete_time": "", "delete_record_id": "",
		"confidence": f.Confidence, "type": "fact", "info": factInfo,
		"graph_id": uuid.New().String(), "importance_score": 1.0, "retrieval_count": 0,
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return db.MemoryInsertNode{}, "", false
	}
	return db.MemoryInsertNode{ID: ltID, PropertiesJSON: propsJSON, EmbeddingVec: ef.embVec}, ltID, true
}

// insertAndLinkLTMNodes inserts allNodes and creates EXTRACTED_FROM edges to WM source nodes.
func (r *Reorganizer) insertAndLinkLTMNodes(ctx context.Context, allNodes []db.MemoryInsertNode, processedWMIDs []string, now string, log *slog.Logger) {
	if len(allNodes) == 0 {
		return
	}
	if err := r.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
		log.Warn("mem_read: InsertMemoryNodes failed", slog.Any("error", err))
		return
	}
	for _, ltmNode := range allNodes {
		for _, wmID := range processedWMIDs {
			if err := r.postgres.CreateMemoryEdge(ctx, ltmNode.ID, wmID, db.EdgeExtractedFrom, now, ""); err != nil {
				log.Debug("mem_read: create extracted_from edge failed (non-fatal)",
					slog.String("ltm_id", ltmNode.ID), slog.String("wm_id", wmID), slog.Any("error", err))
			}
		}
	}
}

// fetchMemReadCandidates fetches dedup candidates for the mem_read pipeline (two-tier).
func (r *Reorganizer) fetchMemReadCandidates(ctx context.Context, conversation, cubeID, agentID string, log *slog.Logger) []llm.Candidate {
	const candidateLimit = 10
	head := conversation
	if len(head) > memReadCandidateHeadChars {
		head = head[:memReadCandidateHeadChars]
	}
	convEmbs, err := r.embedder.Embed(ctx, []string{head})
	if err != nil || len(convEmbs) == 0 || len(convEmbs[0]) == 0 {
		log.Debug("mem_read: embed for candidates failed", slog.Any("error", err))
		return nil
	}
	embedding := convEmbs[0]
	seen := make(map[string]struct{})
	out := make([]llm.Candidate, 0, candidateLimit)

	r.appendVSetCandidates(ctx, cubeID, embedding, candidateLimit, &out, seen, log)
	r.appendPGCandidates(ctx, cubeID, agentID, embedding, candidateLimit, &out, seen, log)
	return out
}

// appendVSetCandidates adds VSET hot-cache candidates to the output slice.
func (r *Reorganizer) appendVSetCandidates(ctx context.Context, cubeID string, embedding []float32, limit int, out *[]llm.Candidate, seen map[string]struct{}, log *slog.Logger) {
	if r.wmCache == nil {
		return
	}
	results, err := r.wmCache.VSim(ctx, cubeID, embedding, limit)
	if err != nil {
		log.Debug("mem_read: vset vsim failed", slog.Any("error", err))
		return
	}
	for _, vr := range results {
		if vr.ID != "" && vr.Memory != "" {
			*out = append(*out, llm.Candidate{ID: vr.ID, Memory: vr.Memory})
			seen[vr.ID] = struct{}{}
		}
	}
}

// appendPGCandidates adds Postgres pgvector candidates to the output slice.
func (r *Reorganizer) appendPGCandidates(ctx context.Context, cubeID, agentID string, embedding []float32, limit int, out *[]llm.Candidate, seen map[string]struct{}, log *slog.Logger) {
	results, err := r.postgres.VectorSearch(ctx, embedding, cubeID, cubeID,
		[]string{"LongTermMemory", "UserMemory"}, agentID, limit)
	if err != nil {
		log.Debug("mem_read: postgres vector search failed", slog.Any("error", err))
		return
	}
	for _, vr := range results {
		id, mem := extractIDAndMemory(vr.Properties)
		if id == "" || mem == "" || isDup(id, seen) {
			continue
		}
		*out = append(*out, llm.Candidate{ID: id, Memory: mem})
		seen[id] = struct{}{}
	}
}

// isDup returns true if id is already in the seen set.
func isDup(id string, seen map[string]struct{}) bool {
	_, dup := seen[id]
	return dup
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
func (r *Reorganizer) processRawMemoryLegacy(ctx context.Context, userID, cubeID string, wmIDs []string, log *slog.Logger) {
	nodes, err := r.postgres.GetMemoryByPropertyIDs(ctx, wmIDs, cubeID)
	if err != nil || len(nodes) == 0 {
		log.Warn("mem_read: GetMemoryByPropertyIDs failed or returned empty", slog.Any("error", err))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	var inserted, skipped int

	for _, node := range nodes {
		n, skip := r.processLegacyNode(ctx, userID, cubeID, node, now, log)
		if skip {
			skipped++
		} else {
			inserted += n
		}
	}

	log.Info("mem_read: complete",
		slog.Int("wm_nodes_processed", len(nodes)),
		slog.Int("ltm_inserted", inserted),
		slog.Int("skipped", skipped),
	)
}

// processLegacyNode runs the enhance→embed→insert pipeline for a single WM node.
// Returns (ltmInserted, skipped).
func (r *Reorganizer) processLegacyNode(ctx context.Context, userID, cubeID string, node db.MemNode, now string, log *slog.Logger) (int, bool) {
	rawText := node.Text
	wmID := node.ID
	if rawText == "" || wmID == "" {
		return 0, true
	}

	// Fast path: skip LLM for texts that are already clean structured sentences.
	// A raw note with ≥8 words and no JSON/code markers is taken as-is.
	var facts []enhancementFact
	var err error
	if isCleanText(rawText) {
		log.Debug("mem_read: skip llmEnhance (clean text)", slog.String("wm_id", wmID))
		facts = []enhancementFact{{Text: rawText}}
	} else {
		facts, err = r.llmEnhance(ctx, rawText)
	}
	if err != nil {
		log.Warn("mem_read: llmEnhance failed", slog.String("wm_id", wmID), slog.Any("error", err))
		return 0, true
	}
	if len(facts) == 0 {
		log.Debug("mem_read: no facts extracted", slog.String("wm_id", wmID))
		r.deleteWMNode(ctx, cubeID, wmID, log)
		return 0, false
	}

	legacyTexts := make([]string, len(facts))
	for i, f := range facts {
		legacyTexts[i] = f.Text
	}
	embs, err := r.embedder.Embed(ctx, legacyTexts)
	if err != nil {
		log.Warn("mem_read: embed failed", slog.String("wm_id", wmID), slog.Any("error", err))
		return 0, true
	}

	ltmNodes := buildLegacyLTMNodes(facts, embs, userID, cubeID, wmID, now)
	if len(ltmNodes) == 0 {
		r.deleteWMNode(ctx, cubeID, wmID, log)
		return 0, false
	}

	if err := r.postgres.InsertMemoryNodes(ctx, ltmNodes); err != nil {
		log.Warn("mem_read: InsertMemoryNodes failed", slog.String("wm_id", wmID), slog.Any("error", err))
		return 0, true
	}
	for _, ltmNode := range ltmNodes {
		if err := r.postgres.CreateMemoryEdge(ctx, ltmNode.ID, wmID, db.EdgeExtractedFrom, now, ""); err != nil {
			log.Debug("mem_read: create extracted_from edge failed (non-fatal)",
				slog.String("ltm_id", ltmNode.ID), slog.String("wm_id", wmID), slog.Any("error", err))
		}
	}

	r.deleteWMNode(ctx, cubeID, wmID, log)
	return len(ltmNodes), false
}

// buildLegacyLTMNodes constructs LTM insert nodes from enhanced facts and their embeddings.
func buildLegacyLTMNodes(facts []enhancementFact, embs [][]float32, userID, cubeID, wmID, now string) []db.MemoryInsertNode {
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
			"user_name":        cubeID, // partition key (upstream convention)
			"user_id":          userID, // person identity — Phase 2 split
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
	return ltmNodes
}

// isCleanText reports whether rawText is a clean, structured sentence that can be
// stored as-is without LLM enhancement (legacy path fast-skip).
// Criteria: ≥8 words, does not start with JSON/code markers, no curly braces.
func isCleanText(s string) bool {
	if len(s) < 10 {
		return false
	}
	// Reject obvious structured data / code
	trimmed := strings.TrimSpace(s)
	if len(trimmed) == 0 {
		return false
	}
	first := trimmed[0]
	if first == '{' || first == '[' || first == '`' {
		return false
	}
	if strings.Contains(trimmed, "```") || strings.Contains(trimmed, "\n\n\n") {
		return false
	}
	// Require minimum word count
	words := strings.Fields(trimmed)
	return len(words) >= 8
}

// llmEnhance calls the LLM to extract structured facts from a raw WM note (legacy path).
func (r *Reorganizer) llmEnhance(ctx context.Context, rawText string) ([]enhancementFact, error) {
	msgs := []map[string]string{
		{"role": "system", "content": memEnhancementSystemPrompt},
		{"role": "user", "content": "Raw working memory note:\n" + rawText},
	}

	callCtx, cancel := context.WithTimeout(ctx, reorganizerLLMTimeout)
	defer cancel()

	raw, err := r.callLLM(callCtx, msgs, llmCompactMaxTokens)
	if err != nil {
		return nil, err
	}

	raw = stripFences(raw)
	var result struct {
		Memories []enhancementFact `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("parse llm enhance json (%s): %w", truncate(raw, llmTruncateLen), err)
	}
	return result.Memories, nil
}
