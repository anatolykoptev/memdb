package handlers

// feedback_ops.go — feedback operation execution (keyword replace, semantic, pure add).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// processKeywordReplace does global find-and-replace across all memories.
func (h *Handler) processKeywordReplace(ctx context.Context, cubeID string, result *llm.KeywordReplaceResult) ([]addResponseItem, error) {
	if result.Original == "" || result.Target == "" {
		return nil, nil
	}

	memTypes := []string{"LongTermMemory", "UserMemory"}
	results, err := h.postgres.FulltextSearch(ctx, result.Original, cubeID, cubeID, memTypes, "", 100)
	if err != nil {
		return nil, fmt.Errorf("keyword replace search: %w", err)
	}

	var updated int
	for _, r := range results {
		id, memory := extractIDAndMemory(r.Properties)
		if id == "" || memory == "" || !strings.Contains(memory, result.Original) {
			continue
		}

		newMemory := strings.ReplaceAll(memory, result.Original, result.Target)

		// Re-embed the changed memory
		vecs, err := h.embedder.Embed(ctx, []string{newMemory})
		if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
			h.logger.Debug("keyword replace: embed failed", slog.String("id", id), slog.Any("error", err))
			continue
		}

		embVec := db.FormatVector(vecs[0])
		now := nowTimestamp()
		if err := h.postgres.UpdateMemoryNodeFull(ctx, id, newMemory, embVec, now); err != nil {
			h.logger.Debug("keyword replace: update failed", slog.String("id", id), slog.Any("error", err))
			continue
		}
		updated++
	}

	h.logger.Info("feedback: keyword replace complete",
		slog.String("original", result.Original),
		slog.String("target", result.Target),
		slog.Int("updated", updated))

	return nil, nil
}

// processSemanticFeedback handles feedback with extracted corrected info.
// Finds related existing memories, decides operations, validates, and executes.
func (h *Handler) processSemanticFeedback(
	ctx context.Context,
	cubeID, userID string,
	items []feedbackItem,
	chatHistory, now string,
) ([]addResponseItem, error) {
	// Collect all corrected info texts for embedding
	texts := make([]string, 0, len(items))
	for _, item := range items {
		texts = append(texts, item.correctedInfo)
	}

	// Batch embed
	vecs, err := h.embedder.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("feedback embed: %w", err)
	}

	// For each item: find related memories → decide ops → execute
	memTypes := []string{"LongTermMemory", "UserMemory"}
	var allItems []addResponseItem

	for i, item := range items {
		if i >= len(vecs) || len(vecs[i]) == 0 {
			continue
		}

		// Find existing memories related to this feedback
		results, err := h.postgres.VectorSearch(ctx, vecs[i], cubeID, cubeID, memTypes, "", 10)
		if err != nil {
			h.logger.Debug("feedback: vector search failed", slog.Any("error", err))
			continue
		}

		// Format existing memories for LLM
		currentMemories := formatSearchResultsForLLM(results)

		// Build chat history context with feedback
		chatCtx := chatHistory + "\nuser feedback: " + item.correctedInfo

		// Decide operations
		ops, err := llm.DecideMemoryOperations(ctx, h.llmChat, currentMemories, item.correctedInfo, chatCtx, now)
		if err != nil {
			h.logger.Debug("feedback: decide ops failed", slog.Any("error", err))
			continue
		}

		// Filter UPDATE ops through safety judgement
		ops = h.validateUpdateOps(ctx, ops, results)

		// Execute operations
		execItems, err := h.executeMemoryOps(ctx, cubeID, userID, ops, item.tags, now)
		if err != nil {
			h.logger.Debug("feedback: execute ops failed", slog.Any("error", err))
			continue
		}
		allItems = append(allItems, execItems...)
	}

	return allItems, nil
}

// validateUpdateOps filters UPDATE operations through the safety judgement LLM.
// Drops UPDATEs with hallucinated IDs or that fail safety checks.
func (h *Handler) validateUpdateOps(ctx context.Context, ops []llm.MemoryOperation, candidates []db.VectorSearchResult) []llm.MemoryOperation {
	// Build set of valid IDs from search results
	validIDs := make(map[string]bool, len(candidates))
	for _, r := range candidates {
		id, _ := extractIDAndMemory(r.Properties)
		if id != "" {
			validIDs[id] = true
		}
	}

	// Filter: drop hallucinated IDs, collect UPDATEs for safety check
	var updates []llm.MemoryOperation
	var filtered []llm.MemoryOperation

	for _, op := range ops {
		switch op.Operation {
		case "UPDATE":
			if !validIDs[op.ID] {
				h.logger.Debug("feedback: dropping UPDATE with hallucinated ID", slog.String("id", op.ID))
				continue
			}
			updates = append(updates, op)
		case "ADD":
			filtered = append(filtered, op)
		case "NONE":
			// skip
		}
	}

	if len(updates) == 0 {
		return filtered
	}

	// Run safety judgement on UPDATE ops
	opsJSON, err := json.Marshal(map[string]any{"operations": updates})
	if err != nil {
		return append(filtered, updates...) // on error, pass through
	}

	judgements, err := llm.JudgeUpdateSafety(ctx, h.llmChat, string(opsJSON))
	if err != nil {
		h.logger.Debug("feedback: safety judgement failed, passing through", slog.Any("error", err))
		return append(filtered, updates...)
	}

	for _, j := range judgements {
		if j.Judgement == "UPDATE_APPROVED" {
			filtered = append(filtered, llm.MemoryOperation{
				ID:        j.ID,
				Text:      j.Text,
				Operation: "UPDATE",
				OldMemory: j.OldMemory,
			})
		} else {
			h.logger.Debug("feedback: UPDATE rejected by safety check",
				slog.String("id", j.ID), slog.String("judgement", j.Judgement))
		}
	}

	return filtered
}

// executeMemoryOps executes ADD and UPDATE operations.
func (h *Handler) executeMemoryOps(ctx context.Context, cubeID, userID string, ops []llm.MemoryOperation, tags []string, now string) ([]addResponseItem, error) {
	var items []addResponseItem
	var nodes []db.MemoryInsertNode

	// Collect texts for batch embedding
	var embedTexts []string
	var embedIndices []int
	for i, op := range ops {
		if op.Operation == "ADD" || op.Operation == "UPDATE" {
			embedTexts = append(embedTexts, op.Text)
			embedIndices = append(embedIndices, i)
		}
	}

	if len(embedTexts) == 0 {
		return nil, nil
	}

	vecs, err := h.embedder.Embed(ctx, embedTexts)
	if err != nil {
		return nil, fmt.Errorf("feedback execute embed: %w", err)
	}

	embedMap := make(map[int][]float32, len(embedIndices))
	for j, idx := range embedIndices {
		if j < len(vecs) && len(vecs[j]) > 0 {
			embedMap[idx] = vecs[j]
		}
	}

	for i, op := range ops {
		vec, ok := embedMap[i]
		if !ok {
			continue
		}
		embVec := db.FormatVector(vec)

		switch op.Operation {
		case "ADD":
			id := uuid.New().String()
			props := map[string]any{
				"id":          id,
				"memory":      op.Text,
				"memory_type": "LongTermMemory",
				// user_name is the cube partition key (upstream MemOS convention; populated from cube_id)
				"user_name":  cubeID,
				"user_id":    userID,
				"status":     "activated",
				"created_at": now,
				"updated_at": now,
				"confidence": 0.9,
				"source":     "feedback",
				"tags":       tags,
			}
			propsJSON, err := json.Marshal(props)
			if err != nil {
				continue
			}
			nodes = append(nodes, db.MemoryInsertNode{ID: id, PropertiesJSON: propsJSON, EmbeddingVec: embVec})
			items = append(items, addResponseItem{
				Memory: op.Text, MemoryID: id, MemoryType: "LongTermMemory", CubeID: cubeID,
			})

		case "UPDATE":
			if err := h.postgres.UpdateMemoryNodeFull(ctx, op.ID, op.Text, embVec, now); err != nil {
				h.logger.Debug("feedback: update memory failed", slog.String("id", op.ID), slog.Any("error", err))
			} else {
				h.logger.Debug("feedback: updated memory", slog.String("id", op.ID))
			}
		}
	}

	if len(nodes) > 0 {
		if err := h.postgres.InsertMemoryNodes(ctx, nodes); err != nil {
			return nil, fmt.Errorf("feedback insert: %w", err)
		}
	}

	return items, nil
}

// processPureAdd runs normal extraction pipeline on feedback text.
func (h *Handler) processPureAdd(ctx context.Context, cubeID, userID, feedbackContent string) ([]addResponseItem, error) {
	if h.llmExtractor == nil {
		return nil, nil
	}

	facts, err := h.llmExtractor.ExtractFacts(ctx, "user: "+feedbackContent)
	if err != nil || len(facts) == 0 {
		return nil, err
	}

	// Embed all facts
	texts := make([]string, 0, len(facts))
	for _, f := range facts {
		if f.Memory != "" {
			texts = append(texts, f.Memory)
		}
	}
	vecs, err := h.embedder.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("pure add embed: %w", err)
	}

	now := nowTimestamp()
	var nodes []db.MemoryInsertNode
	var items []addResponseItem
	vecIdx := 0

	for _, f := range facts {
		if f.Memory == "" || vecIdx >= len(vecs) || len(vecs[vecIdx]) == 0 {
			vecIdx++
			continue
		}

		id := uuid.New().String()
		embVec := db.FormatVector(vecs[vecIdx])
		vecIdx++

		props := map[string]any{
			"id":          id,
			"memory":      f.Memory,
			"memory_type": f.Type,
			// user_name is the cube partition key (upstream MemOS convention; populated from cube_id)
			"user_name":  cubeID,
			"user_id":    userID,
			"status":     "activated",
			"created_at": now,
			"updated_at": now,
			"confidence": f.Confidence,
			"source":     "feedback_pure_add",
			"tags":       f.Tags,
		}
		// D6/D8: carry audit + taxonomy fields through feedback pure-add path.
		if f.RawText != "" {
			props["raw_text"] = f.RawText
		}
		if f.PreferenceCategory != "" {
			props["preference_category"] = f.PreferenceCategory
		}
		propsJSON, err := json.Marshal(props)
		if err != nil {
			continue
		}
		nodes = append(nodes, db.MemoryInsertNode{ID: id, PropertiesJSON: propsJSON, EmbeddingVec: embVec})
		items = append(items, addResponseItem{
			Memory: f.Memory, MemoryID: id, MemoryType: f.Type, CubeID: cubeID,
		})
	}

	if len(nodes) > 0 {
		if err := h.postgres.InsertMemoryNodes(ctx, nodes); err != nil {
			return nil, fmt.Errorf("pure add insert: %w", err)
		}
	}

	return items, nil
}

// formatSearchResultsForLLM formats vector search results as "id": "memory" pairs.
func formatSearchResultsForLLM(results []db.VectorSearchResult) string {
	var sb strings.Builder
	for _, r := range results {
		id, mem := extractIDAndMemory(r.Properties)
		if id != "" && mem != "" {
			fmt.Fprintf(&sb, "%q: %q\n", id, mem)
		}
	}
	return sb.String()
}
