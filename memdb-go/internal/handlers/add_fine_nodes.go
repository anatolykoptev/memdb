package handlers

// add_fine_nodes.go — leaf WM/LTM node builders + batched embedding for the
// fine-add pipeline. Split from add_fine.go (M11 R1). buildAddNodes /
// buildUpdateWMNode signatures are pinned by phase35_test.go.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// embedFacts embeds all ADD/UPDATE facts in one batched ONNX call (~100 ms
// regardless of N). DELETE facts pass through.
func (h *Handler) embedFacts(ctx context.Context, facts []llm.ExtractedFact) []embeddedFact {
	out := make([]embeddedFact, len(facts))
	for i, f := range facts {
		out[i].fact = f
	}
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

// buildUpdateWMNode creates a WorkingMemory node for an UPDATE fact.
// Returns (node, vsetInsert, ok) — ok=false if the embedding is missing.
//
// observationDate (M12.1) is the in-conversation date (YYYY-MM-DD) of the
// latest source message; "" when not available. Stamped onto top-level props
// alongside created_at so retrieval clients can prefix candidates with the
// real conversation date instead of server wall-clock at ingest.
//
//nolint:revive // signature intentionally positional, see file header
func buildUpdateWMNode(
	f llm.ExtractedFact,
	ef embeddedFact,
	cubeID, userID, agentID, sessionID, now string,
	info map[string]any,
	customTags []string,
	sources []map[string]any,
	key string,
	observationDate string,
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
		Key:             key,
		ObservationDate: observationDate,
	}))
	if err != nil {
		return db.MemoryInsertNode{}, wmVSetInsert{}, false
	}
	node := db.MemoryInsertNode{ID: wmID, PropertiesJSON: wmJSON, EmbeddingVec: ef.embVec}
	vsi := wmVSetInsert{id: wmID, memory: f.Memory, embedding: ef.embedding}
	return node, vsi, true
}

// buildAddNodes creates the WM + LTM node pair for a new fact.
// Returns nil nodes + nil item if embVec is empty (embed failed).
//
// observationDate (M12.1) is the in-conversation date (YYYY-MM-DD) of the
// latest source message; "" when not available. Stamped onto top-level props
// of both WM and LTM nodes so retrieval clients see the real conversation
// date instead of server wall-clock at ingest.
//
// Positional signature pinned by phase35_test.go (TestBuildAddNodes_*).
//
//nolint:revive // signature pinned by tests, see file header
func buildAddNodes(
	f llm.ExtractedFact, embVec string, embedding []float32,
	cubeID, userID, agentID, sessionID, now string,
	info map[string]any, customTags []string,
	sources []map[string]any,
	key string,
	observationDate string,
) ([]db.MemoryInsertNode, *addResponseItem) {
	_ = embedding // pinned-signature parameter; reserved for future per-fact telemetry
	if embVec == "" {
		return nil, nil
	}
	createdAt := now
	if f.ValidAt != "" {
		createdAt = f.ValidAt
	}
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
	allTags := append([]string{}, customTags...)
	allTags = append(allTags, f.Tags...)
	wmJSON, err1 := marshalProps(buildNodeProps(memoryNodeProps{
		ID: wmID, Memory: f.Memory, MemoryType: "WorkingMemory",
		UserName: cubeID, UserID: userID, AgentID: agentID, SessionID: sessionID,
		Mode: modeFine, Now: now, CreatedAt: createdAt,
		Info: factInfo, CustomTags: allTags, Sources: sources, Background: "",
		RawText: f.RawText, PreferenceCategory: f.PreferenceCategory,
		Key:             key,
		ObservationDate: observationDate,
	}))
	ltJSON, err2 := marshalProps(buildNodeProps(memoryNodeProps{
		ID: ltID, Memory: f.Memory, MemoryType: f.Type,
		UserName: cubeID, UserID: userID, AgentID: agentID, SessionID: sessionID,
		Mode: modeFine, Now: now, CreatedAt: createdAt,
		Info: factInfo, CustomTags: allTags, Sources: sources, Background: background,
		RawText: f.RawText, PreferenceCategory: f.PreferenceCategory,
		Key:             key,
		ObservationDate: observationDate,
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
