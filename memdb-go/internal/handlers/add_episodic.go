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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/llm"
)

const episodicMemoryType = "EpisodicMemory"

// generateEpisodicSummary asynchronously creates an EpisodicMemory node for the session.
// Called after fact insertion — non-blocking (fire-and-forget via goroutine).
// The node captures a 3-5 sentence overview of the conversation window.
func (h *Handler) generateEpisodicSummary(cubeID, sessionID, conversation, now string) {
	if h.llmExtractor == nil || h.postgres == nil || h.embedder == nil {
		return
	}
	if len(strings.TrimSpace(conversation)) < 100 {
		return // too short to be worth summarising
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		summary, err := callEpisodicSummarizer(ctx, conversation)
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
			"id":            id,
			"memory":        summary,
			"memory_type":   episodicMemoryType,
			"user_name":     cubeID,
			"session_id":    sessionID,
			"status":        "activated",
			"created_at":    now,
			"updated_at":    now,
			"confidence":    0.9,
			"source":        "episodic_summarizer",
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

// linkEntitiesAsync fires a background goroutine that upserts entity_nodes and creates
// MENTIONS_ENTITY edges for every ADD/UPDATE fact that carries LLM-extracted entities.
// Non-blocking, non-fatal — entity graph enriches search but is not required for correctness.
func (h *Handler) linkEntitiesAsync(embedded []embeddedFact, cubeID, now string) {
	if h.postgres == nil {
		return
	}
	// Collect (ltmID, entities) pairs — only ADD/UPDATE facts have LTM nodes.
	type pair struct {
		ltmID     string
		entities  []llm.EntityMention
		relations []llm.EntityRelation
		validAt   string // ISO-8601 when this fact became true (from ExtractedFact.ValidAt)
	}
	var pairs []pair
	for _, ef := range embedded {
		if ef.fact.Action != llm.MemAdd && ef.fact.Action != llm.MemUpdate {
			continue
		}
		if len(ef.fact.Entities) == 0 {
			continue
		}
		// LTM node is the second node in the pair built by buildAddNodes (index 1).
		// We stored it in ef.ltmID during applyFineActions.
		if ef.ltmID == "" {
			continue
		}
		pairs = append(pairs, pair{
			ltmID:     ef.ltmID,
			entities:  ef.fact.Entities,
			relations: ef.fact.Relations,
			validAt:   ef.fact.ValidAt,
		})
	}
	if len(pairs) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// Batch-embed all unique entity names for identity resolution.
		// Falls back to plain upsert if embedder is unavailable.
		entityEmbByName := make(map[string]string)
		if h.embedder != nil {
			var allNames []string
			seen := make(map[string]bool)
			for _, p := range pairs {
				for _, ent := range p.entities {
					if ent.Name != "" && !seen[ent.Name] {
						allNames = append(allNames, ent.Name)
						seen[ent.Name] = true
					}
				}
			}
			if len(allNames) > 0 {
				vecs, err := h.embedder.Embed(ctx, allNames)
				if err == nil && len(vecs) == len(allNames) {
					for i, name := range allNames {
						entityEmbByName[name] = db.FormatVector(vecs[i])
					}
				}
			}
		}

		for _, p := range pairs {
			// Build entity name → normalized ID map for relation linking.
			entityIDByName := make(map[string]string, len(p.entities))
			for _, ent := range p.entities {
				if ent.Name == "" {
					continue
				}
				embVec := entityEmbByName[ent.Name]
				entityID, err := h.postgres.UpsertEntityNodeWithEmbedding(ctx, ent.Name, ent.Type, cubeID, now, embVec)
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
			// Create entity-to-entity triplet edges from LLM-extracted relations.
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
	}()
}

// callEpisodicSummarizer sends a single chat completion request to generate the session summary.
func callEpisodicSummarizer(ctx context.Context, conversation string) (string, error) {
	// Truncate to avoid prompt overflows (last 6000 chars covers ~4000 tokens)
	if len(conversation) > 6000 {
		conversation = "..." + conversation[len(conversation)-6000:]
	}

	payload := map[string]any{
		"model":       llmDefaultModel,
		"temperature": 0.2,
		"max_tokens":  300,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a memory archivist. Summarize the key facts and themes from the conversation in 3-5 concise sentences. Focus on factual information, not pleasantries. Do not use bullet points.",
			},
			{
				"role":    "user",
				"content": "Conversation:\n" + conversation + "\n\nWrite a 3-5 sentence episodic summary capturing what was discussed and decided:",
			},
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, llmProxyURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if llmProxyAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+llmProxyAPIKey)
	}

	resp, err := llmClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return "", err
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("episodic summarizer: bad response")
	}

	// Validate we got a reference to the llm package (suppress unused import)
	_ = llm.MinConfidence

	return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
}
