package scheduler

// reorganizer_mem_read_legacy.go — legacy mem_read fallback (used when
// llmExtractor is nil): simple per-node llmEnhance → embed → insert LTM pipeline.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// enhancementFact is one structured memory extracted by the LLM from a raw WM note (legacy path).
type enhancementFact struct {
	Text string `json:"text"`
	Type string `json:"type"` // "LongTermMemory" or "UserMemory"
}

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

	raw = string(llm.StripJSONFence([]byte(raw)))
	var result struct {
		Memories []enhancementFact `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("parse llm enhance json (%s): %w", truncate(raw, llmTruncateLen), err)
	}
	return result.Memories, nil
}
