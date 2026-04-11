package handlers

// add_episodic.go — background episodic session summary generation.
//
// After a fine-mode add completes, a background goroutine generates a compact
// (3-5 sentence) summary of what was discussed and stores it as an EpisodicMemory node.
//
// Why this matters for benchmarks:
//   Memobase achieves 85% LOCOMO temporal accuracy largely by maintaining episodic
//   summaries that allow multi-hop reasoning ("what did we discuss in session 3?").
//   A single EpisodicMemory node covering 20 conversation turns is much easier to
//   retrieve than 20 individual LongTermMemory facts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	episodicMemoryType     = "EpisodicMemory"
	episodicSummaryTimeout = 45 * time.Second
	entityLinkTimeout      = 15 * time.Second
	episodicConvMaxChars   = 6000 // ~4000 tokens; truncate to avoid prompt overflow
	episodicMaxTokens      = 300  // max tokens for episodic summary response
)

// generateEpisodicSummary asynchronously creates an EpisodicMemory node for the session.
// Called after fact insertion — non-blocking (fire-and-forget via goroutine).
// The node captures a 3-5 sentence overview of the conversation window.
func (h *Handler) generateEpisodicSummary(cubeID, userID, sessionID, conversation, now string, factCount int) {
	if h.llmExtractor == nil || h.llmChat == nil || h.postgres == nil || h.embedder == nil {
		return
	}
	if factCount == 0 {
		return // no facts extracted — nothing to summarize
	}
	if codeBlockRatio(conversation) > episodicCodeRatio {
		return // mostly code — low episodic value
	}
	if len(strings.TrimSpace(conversation)) < 100 {
		return // too short to be worth summarising
	}
	// Detect session type for focused summary.
	sessionType := detectSessionType(conversation)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), episodicSummaryTimeout)
		defer cancel()

		summary, err := callEpisodicSummarizer(ctx, h.llmChat, conversation, sessionType)
		if err != nil {
			h.logger.Debug("episodic summary: llm call failed", slog.Any("error", err))
			return
		}
		if summary == "" {
			return
		}

		// Embed the summary
		vecs, err := h.embedder.Embed(ctx, []string{summary})
		if err != nil || len(vecs) == 0 {
			h.logger.Debug("episodic summary: embed failed", slog.Any("error", err))
			return
		}
		vec := vecs[0]

		// Build node properties
		id := uuid.New().String()
		props := map[string]any{
			"id":          id,
			"memory":      summary,
			"memory_type": episodicMemoryType,
			// user_name is the cube partition key (upstream MemOS convention; populated from cube_id)
			"user_name":  cubeID,
			"user_id":    userID,
			"session_id": sessionID,
			"status":     "activated",
			"created_at": now,
			"updated_at": now,
			"confidence": 0.9,
			"source":     "episodic_summarizer",
		}
		propsJSON, err := json.Marshal(props)
		if err != nil {
			return
		}

		// Format embedding as pgvector literal
		vecParts := make([]string, len(vec))
		for i, v := range vec {
			vecParts[i] = fmt.Sprintf("%g", v)
		}
		embStr := "[" + strings.Join(vecParts, ",") + "]"

		node := db.MemoryInsertNode{
			ID:             id,
			PropertiesJSON: propsJSON,
			EmbeddingVec:   embStr,
		}
		if err := h.postgres.InsertMemoryNodes(ctx, []db.MemoryInsertNode{node}); err != nil {
			h.logger.Debug("episodic summary: insert failed", slog.Any("error", err))
			return
		}
		h.logger.Debug("episodic summary: stored",
			slog.String("cube_id", cubeID),
			slog.String("session_id", sessionID),
			slog.Int("summary_len", len(summary)),
		)
	}()
}

// entityLinkPair holds a handler-side LTM node and its associated entities/relations.
type entityLinkPair struct {
	ltmID     string
	entities  []llm.EntityMention
	relations []llm.EntityRelation
	validAt   string
}

// linkEntitiesAsync fires a background goroutine that upserts entity_nodes and creates
// MENTIONS_ENTITY edges for every ADD/UPDATE fact that carries LLM-extracted entities.
// Non-blocking, non-fatal — entity graph enriches search but is not required for correctness.
func (h *Handler) linkEntitiesAsync(embedded []embeddedFact, cubeID, now string) {
	if h.postgres == nil {
		return
	}
	pairs := collectHandlerEntityPairs(embedded)
	if len(pairs) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), entityLinkTimeout)
		defer cancel()

		embByName := h.batchEmbedHandlerEntities(ctx, pairs)
		for _, p := range pairs {
			h.linkHandlerPair(ctx, p, cubeID, now, embByName)
		}
	}()
}

// collectHandlerEntityPairs builds entityLinkPair slice from embedded facts.
func collectHandlerEntityPairs(embedded []embeddedFact) []entityLinkPair {
	var pairs []entityLinkPair
	for _, ef := range embedded {
		if ef.fact.Action != llm.MemAdd && ef.fact.Action != llm.MemUpdate {
			continue
		}
		if len(ef.fact.Entities) == 0 || ef.ltmID == "" {
			continue
		}
		pairs = append(pairs, entityLinkPair{
			ltmID: ef.ltmID, entities: ef.fact.Entities,
			relations: ef.fact.Relations, validAt: ef.fact.ValidAt,
		})
	}
	return pairs
}

// batchEmbedHandlerEntities embeds all unique entity names, returns name→vecStr map.
func (h *Handler) batchEmbedHandlerEntities(ctx context.Context, pairs []entityLinkPair) map[string]string {
	embByName := make(map[string]string)
	if h.embedder == nil {
		return embByName
	}
	seen := make(map[string]bool)
	var allNames []string
	for _, p := range pairs {
		for _, ent := range p.entities {
			if ent.Name != "" && !seen[ent.Name] {
				allNames = append(allNames, ent.Name)
				seen[ent.Name] = true
			}
		}
	}
	if len(allNames) == 0 {
		return embByName
	}
	vecs, err := h.embedder.Embed(ctx, allNames)
	if err == nil && len(vecs) == len(allNames) {
		for i, name := range allNames {
			embByName[name] = db.FormatVector(vecs[i])
		}
	}
	return embByName
}

// linkHandlerPair upserts entity nodes, creates MENTIONS_ENTITY and entity-relation edges.
func (h *Handler) linkHandlerPair(ctx context.Context, p entityLinkPair, cubeID, now string, embByName map[string]string) {
	entityIDByName := make(map[string]string, len(p.entities))
	for _, ent := range p.entities {
		if ent.Name == "" {
			continue
		}
		entityID, err := h.postgres.UpsertEntityNodeWithEmbedding(ctx, ent.Name, ent.Type, cubeID, now, embByName[ent.Name])
		if err != nil {
			h.logger.Debug("entity link: upsert entity node failed",
				slog.String("name", ent.Name), slog.Any("error", err))
			continue
		}
		entityIDByName[ent.Name] = entityID
		if err := h.postgres.CreateMemoryEdge(ctx, p.ltmID, entityID, db.EdgeMentionsEntity, now, p.validAt); err != nil {
			h.logger.Debug("entity link: create edge failed",
				slog.String("ltm_id", p.ltmID), slog.String("entity_id", entityID), slog.Any("error", err))
		}
	}
	for _, rel := range p.relations {
		fromID, ok1 := entityIDByName[rel.Subject]
		toID, ok2 := entityIDByName[rel.Object]
		if !ok1 || !ok2 || rel.Predicate == "" {
			continue
		}
		if err := h.postgres.UpsertEntityEdge(ctx, fromID, rel.Predicate, toID, p.ltmID, cubeID, p.validAt, now); err != nil {
			h.logger.Debug("entity link: upsert entity edge failed",
				slog.String("from", fromID), slog.String("pred", rel.Predicate),
				slog.String("to", toID), slog.Any("error", err))
		}
	}
}

// callEpisodicSummarizer sends a single chat completion request to generate the session summary.
// sessionType customizes the summary prompt focus (decision/learning/debug/planning/general).
// client must be non-nil; it is used to call the LLM via the shared llm.Client (CLIProxyAPI).
func callEpisodicSummarizer(ctx context.Context, client *llm.Client, conversation, sessionType string) (string, error) {
	// Truncate to avoid prompt overflows (last episodicConvMaxChars covers ~4000 tokens)
	if len(conversation) > episodicConvMaxChars {
		conversation = "..." + conversation[len(conversation)-episodicConvMaxChars:]
	}

	systemContent := "You are a memory archivist. Summarize the key facts and themes from the conversation in 3-5 concise sentences. Focus on factual information, not pleasantries. Do not use bullet points."
	if focus := sessionPromptFocus(sessionType); focus != "" {
		systemContent += "\n\n" + focus
	}

	messages := []map[string]string{
		{"role": "system", "content": systemContent},
		{"role": "user", "content": "Conversation:\n" + conversation + "\n\nWrite a 3-5 sentence episodic summary capturing what was discussed and decided:"},
	}

	summary, err := client.Chat(ctx, messages, episodicMaxTokens)
	if err != nil {
		return "", fmt.Errorf("episodic summarizer: %w", err)
	}
	if summary == "" {
		return "", errors.New("episodic summarizer: empty response")
	}
	return strings.TrimSpace(summary), nil
}
