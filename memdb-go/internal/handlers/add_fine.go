package handlers

// add_fine.go — fine-mode memory add pipeline (LLM-based).
// Responsibility: format conversation, fetch dedup candidates, call ExtractAndDedup
// (one unified LLM call), embed results in parallel, apply add/update/delete actions.
// Uses: llmExtractor, embedder, postgres.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// candidateConvHeadChars is the character limit for the conversation head used
// when fetching dedup candidates (embed only a prefix for speed).
const candidateConvHeadChars = 512

// nativeFineAddForCube implements the fine-mode add pipeline (v2) for a single cube.
//
// Pipeline:
//  1. Format messages → conversation text
//  2. Embed truncated conversation → vector search top-10 → dedup candidates
//  3. ONE LLM call: ExtractAndDedup(conversation, candidates)
//     → []ExtractedFact{memory, type, action, confidence, target_id, valid_at}
//  4. Embed ADD/UPDATE facts in parallel (DELETE needs no embedding)
//  5. ADD   → insert WM + LTM nodes
//     UPDATE → UpdateMemoryNodeFull (merged text + re-embedding)
//     DELETE → soft-invalidate contradicted memory
//  6. Cleanup old WorkingMemory
func (h *Handler) nativeFineAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	if len(req.Messages) == 0 {
		return nil, nil
	}

	now := nowTimestamp()
	sessionID := stringOrEmpty(req.SessionID)
	info := mapOrEmpty(req.Info)

	// Step 1: format conversation
	conversation := formatConversation(req.Messages, now)

	// Step 1.5: content router — classify and generate quality hints
	sig := classifyContent(req.Messages, conversation)
	if sig.Skip {
		h.logger.Debug("fine add: skipped extraction",
			slog.String("reason", sig.SkipReason), slog.String("cube_id", cubeID))
		return nil, nil
	}

	// Step 2: candidate fetch for dedup context
	candidates, topScore := h.fetchFineCandidates(ctx, conversation, cubeID, stringOrEmpty(req.AgentID))
	if topScore > nearDuplicateThreshold {
		h.logger.Debug("fine add: skipped — near-duplicate",
			slog.Float64("top_score", topScore), slog.String("cube_id", cubeID))
		return nil, nil
	}

	// Step 2.5: merge hint for high-similarity (but not duplicate) content
	if topScore > mergeSuggestionThreshold {
		sig.Hints = append(sig.Hints, "High-similarity existing memory found — prefer UPDATE over ADD if semantically equivalent")
	}

	// Step 3: unified LLM extraction + dedup (one round-trip, with content hints).
	// Prepend date-aware hint when MEMDB_DATE_AWARE_EXTRACT is enabled (default true)
	// so the LLM emits `[mention YYYY-MM-DD]` tags on time-anchored facts.
	// See add_fine_prompt.go and the M9 Stream 4 spec for the rationale.
	hints := append(dateAwareExtractHints(), sig.Hints...)
	facts, err := h.llmExtractor.ExtractAndDedup(ctx, conversation, candidates, hints...)
	if err != nil {
		recordDateAwareExtractOutcome(ctx, dateAwareExtractOutcomeError)
		return nil, fmt.Errorf("fine add: extract and dedup: %w", err)
	}
	recordDateAwareExtractOutcome(ctx, "")
	if len(facts) == 0 {
		h.logger.Debug("fine add: no facts extracted", slog.String("cube_id", cubeID))
		return nil, nil
	}
	h.logger.Debug("fine add: extracted facts",
		slog.Int("count", len(facts)),
		slog.String("model", h.llmExtractor.Model()),
	)

	// Step 3.5: hallucination filter — validate facts against conversation (non-fatal)
	facts = h.filterHallucinatedFacts(ctx, conversation, facts)
	if len(facts) == 0 {
		h.logger.Debug("fine add: all facts filtered as hallucinations", slog.String("cube_id", cubeID))
		return nil, nil
	}

	// Step 4a: filter exact duplicates by content-hash before embedding (saves ONNX inference).
	facts = h.filterAddsByContentHash(ctx, facts, cubeID)

	// Step 4b: parallel embed
	embedded := h.embedFacts(ctx, facts)

	// Step 5: apply actions
	sources := buildSourcesFromMessages(req.Messages)
	allNodes, items, vsetInserts := h.applyFineActions(ctx, embedded, cubeID, *req.UserID, stringOrEmpty(req.AgentID), sessionID, now, info, req.CustomTags, sources, stringOrEmpty(req.Key))

	if len(allNodes) > 0 {
		if err := h.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
			return nil, fmt.Errorf("fine add: insert nodes: %w", err)
		}
		// Write new WorkingMemory nodes to VSET hot cache (non-fatal)
		if h.wmCache != nil {
			ts := nowUnix()
			for _, vi := range vsetInserts {
				if err := h.wmCache.VAdd(ctx, cubeID, vi.id, vi.memory, vi.embedding, ts); err != nil {
					h.logger.Debug("fine add: vset write failed",
						slog.String("id", vi.id), slog.Any("error", err))
				}
			}
		}
		// Entity linking: upsert entity_nodes + MENTIONS_ENTITY edges (async, non-fatal).
		// Each ADD/UPDATE fact may carry entities extracted by the LLM.
		// We link the LTM node (second node in each pair) to each entity.
		h.linkEntitiesAsync(embedded, cubeID, now)
	}

	// Step 6: cleanup
	h.cleanupWorkingMemory(ctx, cubeID)

	// Step 7: generate episodic session summary in background (non-blocking, non-fatal)
	// EpisodicMemory nodes improve multi-hop temporal reasoning on later queries.
	h.generateEpisodicSummary(cubeID, *req.UserID, sessionID, conversation, now, len(facts))

	// Step 8: extract skill memories in background (non-blocking, non-fatal)
	h.generateSkillMemory(cubeID, *req.UserID, conversation, len(req.Messages))

	// Step 8.5: extract tool trajectory in background (non-blocking, non-fatal)
	h.generateToolTrajectory(cubeID, *req.UserID, conversation)

	// Step 9: generate / update User profile summary in background
	if h.profiler != nil {
		h.profiler.TriggerRefresh(cubeID)
	}

	// Step 10: structured user-profile extraction (M10 Stream 2).
	// Fire-and-forget: never blocks the request and never returns errors.
	// Gated on MEMDB_PROFILE_EXTRACT (default true).
	// cubeID is required to keep tenant-isolation (security audit C1).
	h.triggerProfileExtract(conversation, *req.UserID, cubeID)

	return items, nil
}

// filterAddsByContentHash removes ADD facts whose content_hash already exists in the DB.
// Batch-checks all ADD hashes in one round-trip. Non-fatal: on error, all facts pass through.
func (h *Handler) filterAddsByContentHash(ctx context.Context, facts []llm.ExtractedFact, cubeID string) []llm.ExtractedFact {
	// Collect indices and hashes for ADD facts only.
	type entry struct {
		idx  int
		hash string
	}
	var addEntries []entry
	hashes := make([]string, 0, len(facts))
	for i, f := range facts {
		if f.Action != llm.MemAdd && f.Action != "" {
			continue
		}
		if f.Memory == "" {
			continue
		}
		h := textHash(f.Memory)
		addEntries = append(addEntries, entry{idx: i, hash: h})
		hashes = append(hashes, h)
	}
	if len(hashes) == 0 {
		return facts
	}

	existing, err := h.postgres.FilterExistingContentHashes(ctx, hashes, cubeID)
	if err != nil {
		h.logger.Debug("fine add: batch hash check failed (skipping hash dedup)",
			slog.Any("error", err))
		return facts
	}

	skipped := 0
	for _, e := range addEntries {
		if existing[e.hash] {
			// Mark as skip so applyFineActions ignores it.
			facts[e.idx].Action = llm.MemSkip
			skipped++
		} else if facts[e.idx].ContentHash == "" {
			// Embed the content_hash into the fact so buildAddNodes can store it.
			facts[e.idx].ContentHash = e.hash
		}
	}
	if skipped > 0 {
		h.logger.Debug("fine add: skipped exact duplicates by content_hash",
			slog.Int("skipped", skipped))
	}
	return facts
}

// wmVSetInsert represents a WorkingMemory node to be inserted into the VSET cache.
type wmVSetInsert struct {
	id        string
	memory    string
	embedding []float32
}

// fetchFineCandidates embeds the conversation head and returns top-10 existing memories
// as LLM candidates for dedup context.
//
// Strategy (two-tier):
//  1. VSET hot cache (Redis HNSW, ~1-5ms) — covers recent WorkingMemory
//  2. Postgres pgvector fallback (~20-100ms) — covers LongTermMemory + UserMemory
//
// Both results are merged and deduplicated by ID before returning.
func (h *Handler) fetchFineCandidates(ctx context.Context, conversation, cubeID, agentID string) ([]llm.Candidate, float64) { //nolint:gocognit,cyclop
	head := conversation[:min(candidateConvHeadChars, len(conversation))]
	convEmbs, err := h.embedder.Embed(ctx, []string{head})
	if err != nil || len(convEmbs) == 0 || len(convEmbs[0]) == 0 {
		return nil, 0
	}
	embedding := convEmbs[0]

	seen := make(map[string]struct{})
	out := make([]llm.Candidate, 0, 10)
	var topScore float64

	// Tier 1: VSET hot cache (WorkingMemory, HNSW in-memory)
	if h.wmCache != nil { //nolint:nestif
		vsetResults, err := h.wmCache.VSim(ctx, cubeID, embedding, 10)
		if err != nil {
			h.logger.Debug("fine add: vset vsim failed, falling back",
				slog.String("cube_id", cubeID), slog.Any("error", err))
		} else {
			for _, r := range vsetResults {
				if r.Score > topScore {
					topScore = r.Score
				}
				if r.ID != "" && r.Memory != "" {
					out = append(out, llm.Candidate{ID: r.ID, Memory: r.Memory})
					seen[r.ID] = struct{}{}
				}
			}
			h.logger.Debug("fine add: vset candidates",
				slog.Int("count", len(out)), slog.String("cube_id", cubeID))
		}
	}

	// Tier 2: Postgres pgvector (LongTermMemory + UserMemory)
	results, err := h.postgres.VectorSearch(ctx, embedding, cubeID, cubeID,
		[]string{"LongTermMemory", "UserMemory"}, agentID, 10)
	if err != nil {
		h.logger.Debug("fine add: postgres vector search failed",
			slog.String("cube_id", cubeID), slog.Any("error", err))
	} else {
		for _, r := range results {
			if r.Score > topScore {
				topScore = r.Score
			}
			id, mem := extractIDAndMemory(r.Properties)
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

	return out, topScore
}

// embeddedFact pairs an ExtractedFact with its computed embedding (if any).
type embeddedFact struct {
	fact      llm.ExtractedFact
	embedding []float32 // raw float32 slice — used for VSET VAdd
	embVec    string    // pgvector-formatted string — used for postgres insert
	ltmID     string    // LTM node ID assigned during applyFineActions (ADD: new ltID; UPDATE: targetID)
}

// embedFacts embeds all ADD/UPDATE facts in a single batched ONNX inference call.
// DELETE facts are passed through without embedding.
//
// Previously used N parallel goroutines, each calling Embed with one text.
// Because the ONNX session is serialized by a mutex, goroutines queued up and
// ran sequentially anyway — N × ~80ms. A single batched call runs one ONNX
// forward pass for all texts: ~100ms regardless of N (3-4x faster for N≥3).
func (h *Handler) embedFacts(ctx context.Context, facts []llm.ExtractedFact) []embeddedFact {
	out := make([]embeddedFact, len(facts))
	for i, f := range facts {
		out[i].fact = f
	}

	// Collect indices and texts that need embedding (ADD/UPDATE with non-empty memory).
	indices := make([]int, 0, len(facts))
	texts := make([]string, 0, len(facts))
	for i, f := range facts {
		if f.Action == llm.MemDelete || f.Memory == "" {
			continue
		}
		indices = append(indices, i)
		texts = append(texts, f.Memory)
	}
	if len(texts) == 0 {
		return out
	}

	// Single batched inference — one ONNX forward pass for all texts.
	embs, err := h.embedder.Embed(ctx, texts)
	if err != nil {
		h.logger.Debug("fine add: batch embed failed", slog.Any("error", err))
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

// applyFineActions processes each embeddedFact and returns nodes to insert plus response items.
// Side effects: UPDATE and DELETE operations are applied immediately via postgres.
func (h *Handler) applyFineActions(
	ctx context.Context,
	embedded []embeddedFact,
	cubeID, userID, agentID, sessionID, now string,
	info map[string]any,
	customTags []string,
	sources []map[string]any,
	key string,
) ([]db.MemoryInsertNode, []addResponseItem, []wmVSetInsert) {
	var allNodes []db.MemoryInsertNode
	var items []addResponseItem
	var vsetInserts []wmVSetInsert

	for i := range embedded {
		ef := embedded[i]
		f := ef.fact
		switch f.Action {
		case llm.MemSkip:
			continue

		case llm.MemDelete:
			h.applyDeleteAction(ctx, f.TargetID, cubeID)
			h.evictFromVSet(ctx, cubeID, f.TargetID, "fine add: vset vrem failed")

		case llm.MemUpdate:
			h.applyUpdateAction(ctx, f.TargetID, f.Memory, ef.embVec, now)
			embedded[i].ltmID = f.TargetID
			if node, vsi, ok := buildUpdateWMNode(f, ef, cubeID, userID, agentID, sessionID, now, info, customTags, sources, key); ok {
				allNodes = append(allNodes, node)
				vsetInserts = append(vsetInserts, vsi)
			}

		default: // llm.MemAdd
			nodes, item := buildAddNodes(f, ef.embVec, ef.embedding, cubeID, userID, agentID, sessionID, now, info, customTags, sources, key)
			allNodes = append(allNodes, nodes...)
			if item != nil {
				items = append(items, *item)
				if len(nodes) >= 2 {
					embedded[i].ltmID = nodes[1].ID
				}
				if len(ef.embedding) > 0 && len(nodes) > 0 {
					vsetInserts = append(vsetInserts, wmVSetInsert{
						id: nodes[0].ID, memory: f.Memory, embedding: ef.embedding,
					})
				}
			}
		}
	}
	return allNodes, items, vsetInserts
}

// buildUpdateWMNode creates a WorkingMemory node for an UPDATE fact.
// Returns (node, vsetInsert, ok) — ok=false if the embedding is missing.
func buildUpdateWMNode(
	f llm.ExtractedFact,
	ef embeddedFact,
	cubeID, userID, agentID, sessionID, now string,
	info map[string]any,
	customTags []string,
	sources []map[string]any,
	key string,
) (db.MemoryInsertNode, wmVSetInsert, bool) {
	if ef.embVec == "" || len(ef.embedding) == 0 {
		return db.MemoryInsertNode{}, wmVSetInsert{}, false
	}
	wmID := uuid.New().String()
	createdAt := now
	if f.ValidAt != "" {
		createdAt = f.ValidAt
	}
	allTags := append(append([]string{}, customTags...), f.Tags...)
	factInfo := make(map[string]any, len(info)+1)
	for k, v := range info {
		factInfo[k] = v
	}
	if f.ContentHash != "" {
		factInfo["content_hash"] = f.ContentHash
	}
	wmJSON, err := marshalProps(buildNodeProps(memoryNodeProps{
		ID: wmID, Memory: f.Memory, MemoryType: "WorkingMemory",
		UserName: cubeID, UserID: userID, AgentID: agentID, SessionID: sessionID,
		Mode: modeFine, Now: now, CreatedAt: createdAt,
		Info: factInfo, CustomTags: allTags, Sources: sources, Background: "",
		RawText: f.RawText, PreferenceCategory: f.PreferenceCategory,
		Key: key,
	}))
	if err != nil {
		return db.MemoryInsertNode{}, wmVSetInsert{}, false
	}
	node := db.MemoryInsertNode{ID: wmID, PropertiesJSON: wmJSON, EmbeddingVec: ef.embVec}
	vsi := wmVSetInsert{id: wmID, memory: f.Memory, embedding: ef.embedding}
	return node, vsi, true
}

// evictFromVSet removes a memory ID from the VSET hot cache (non-fatal).
func (h *Handler) evictFromVSet(ctx context.Context, cubeID, id, logMsg string) {
	if h.wmCache == nil || id == "" {
		return
	}
	if err := h.wmCache.VRem(ctx, cubeID, id); err != nil {
		h.logger.Debug(logMsg, slog.String("id", id), slog.Any("error", err))
	}
}

// applyDeleteAction hard-deletes a memory contradicted by a newer fact.
// Before deletion, invalidates all outgoing edges (bi-temporal: records when fact stopped being true).
// Hard delete (not soft) so contradicted memories never appear in future search results.
func (h *Handler) applyDeleteAction(ctx context.Context, targetID, cubeID string) {
	if targetID == "" {
		return
	}
	// Bi-temporal invalidation: stamp invalid_at = now on all edges sourced from this memory.
	// Graphiti pattern: edges are preserved for historical audit; invalid_at marks end of validity.
	now := nowTimestamp()
	if err := h.postgres.InvalidateEdgesByMemoryID(ctx, targetID, now); err != nil {
		h.logger.Debug("fine add: invalidate memory edges failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
	if err := h.postgres.InvalidateEntityEdgesByMemoryID(ctx, targetID, now); err != nil {
		h.logger.Debug("fine add: invalidate entity edges failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
	if _, err := h.postgres.DeleteByPropertyIDs(ctx, []string{targetID}, cubeID); err != nil {
		h.logger.Debug("fine add: delete contradicted memory failed",
			slog.String("id", targetID), slog.Any("error", err))
	} else {
		h.logger.Debug("fine add: deleted contradicted memory", slog.String("id", targetID))
	}
	// M10 Stream 6: cascade-clear ce_score_topk on every memory that
	// listed this one as a neighbour (single SQL UPDATE, no per-row loop).
	// The deleted memory itself is gone, so no need to clear its own key.
	if err := h.postgres.ClearCEScoresTopKForNeighbor(ctx, targetID); err != nil {
		h.logger.Debug("fine add: clear ce_score_topk cascade on delete failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
}

// applyUpdateAction merges a fact into an existing memory and re-embeds it.
// Also invalidates old edges from the superseded version (bi-temporal: they will be re-created
// by linkEntitiesAsync with the new valid_at from the updated fact).
func (h *Handler) applyUpdateAction(ctx context.Context, targetID, memory, embVec, now string) {
	if targetID == "" || memory == "" || embVec == "" {
		return
	}
	// Bi-temporal invalidation: old edges from this memory are no longer valid.
	// New edges will be created by linkEntitiesAsync with the updated fact's valid_at.
	if err := h.postgres.InvalidateEdgesByMemoryID(ctx, targetID, now); err != nil {
		h.logger.Debug("fine add: invalidate memory edges on update failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
	if err := h.postgres.InvalidateEntityEdgesByMemoryID(ctx, targetID, now); err != nil {
		h.logger.Debug("fine add: invalidate entity edges on update failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
	if err := h.postgres.UpdateMemoryNodeFull(ctx, targetID, memory, embVec, now); err != nil {
		h.logger.Debug("fine add: update node failed",
			slog.String("id", targetID), slog.Any("error", err))
	} else {
		h.logger.Debug("fine add: merged update", slog.String("target_id", targetID))
	}
	// M10 Stream 6: the memory text changed, so its own cached pairwise
	// scores no longer reflect the new content. Clear the local key AND
	// cascade-clear neighbours that pointed at us.
	if err := h.postgres.ClearCEScoresTopK(ctx, targetID); err != nil {
		h.logger.Debug("fine add: clear ce_score_topk on update failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
	if err := h.postgres.ClearCEScoresTopKForNeighbor(ctx, targetID); err != nil {
		h.logger.Debug("fine add: clear ce_score_topk cascade on update failed (non-fatal)",
			slog.String("id", targetID), slog.Any("error", err))
	}
}

// buildAddNodes creates the WM + LTM node pair for a new fact.
// Returns nil nodes + nil item if embVec is empty (embed failed).
func buildAddNodes(
	f llm.ExtractedFact, embVec string, embedding []float32,
	cubeID, userID, agentID, sessionID, now string,
	info map[string]any, customTags []string,
	sources []map[string]any,
	key string,
) ([]db.MemoryInsertNode, *addResponseItem) {
	if embVec == "" {
		return nil, nil
	}

	// Use LLM-provided valid_at as created_at if present (bi-temporal model).
	createdAt := now
	if f.ValidAt != "" {
		createdAt = f.ValidAt
	}

	// Merge computed content_hash into per-fact info copy.
	factInfo := make(map[string]any, len(info)+1)
	for k, v := range info {
		factInfo[k] = v
	}
	if f.ContentHash != "" {
		factInfo["content_hash"] = f.ContentHash
	}

	wmID := uuid.New().String()
	ltID := uuid.New().String()
	background := workingBinding(wmID)

	// Incorporate Topic/Entity tags
	allTags := append([]string{}, customTags...)
	allTags = append(allTags, f.Tags...)

	wmJSON, err1 := marshalProps(buildNodeProps(memoryNodeProps{
		ID: wmID, Memory: f.Memory, MemoryType: "WorkingMemory",
		UserName: cubeID, UserID: userID, AgentID: agentID, SessionID: sessionID,
		Mode: modeFine, Now: now, CreatedAt: createdAt,
		Info: factInfo, CustomTags: allTags, Sources: sources, Background: "",
		RawText: f.RawText, PreferenceCategory: f.PreferenceCategory,
		Key: key,
	}))
	ltJSON, err2 := marshalProps(buildNodeProps(memoryNodeProps{
		ID: ltID, Memory: f.Memory, MemoryType: f.Type,
		UserName: cubeID, UserID: userID, AgentID: agentID, SessionID: sessionID,
		Mode: modeFine, Now: now, CreatedAt: createdAt,
		Info: factInfo, CustomTags: allTags, Sources: sources, Background: background,
		RawText: f.RawText, PreferenceCategory: f.PreferenceCategory,
		Key: key,
	}))
	if err1 != nil || err2 != nil {
		return nil, nil
	}

	nodes := []db.MemoryInsertNode{
		{ID: wmID, PropertiesJSON: wmJSON, EmbeddingVec: embVec},
		{ID: ltID, PropertiesJSON: ltJSON, EmbeddingVec: embVec},
	}
	item := &addResponseItem{
		Memory:     f.Memory,
		MemoryID:   ltID,
		MemoryType: f.Type,
		CubeID:     cubeID,
	}
	return nodes, item
}

// formatConversation formats messages into the "role: [time]: content\n" text for the LLM.
func formatConversation(messages []chatMessage, fallbackTime string) string {
	var sb strings.Builder
	for _, msg := range messages {
		chatTime := msg.ChatTime
		if chatTime == "" {
			chatTime = fallbackTime
		}
		fmt.Fprintf(&sb, "%s: [%s]: %s\n", msg.Role, chatTime, msg.Content)
	}
	return strings.TrimSpace(sb.String())
}
