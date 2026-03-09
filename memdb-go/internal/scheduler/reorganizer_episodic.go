package scheduler

// reorganizer_episodic.go — background episodic session summary for async mem_read.
//
// Replicates the logic from handlers/add_episodic.go:generateEpisodicSummary as a
// Reorganizer method. Runs in a background goroutine (45s timeout). Non-fatal.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	episodicMemType          = "EpisodicMemory"
	episodicSummaryTimeout   = 45 * time.Second // timeout for background episodic summary goroutine
	episodicConvMaxChars     = 6000             // ~4000 tokens; truncate to avoid prompt overflow
	episodicSummaryMaxTokens = 300              // max_tokens for episodic summary LLM call
)

// generateEpisodicSummary asynchronously creates an EpisodicMemory node for the session.
// Uses r.callLLM() for summarization and r.embedder for embedding.
// Runs in background goroutine (45s timeout). Non-fatal.
func (r *Reorganizer) generateEpisodicSummary(cubeID, sessionID, conversation, now string) {
	if r.embedder == nil {
		return
	}
	if len(strings.TrimSpace(conversation)) < 100 {
		return // too short to be worth summarising
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), episodicSummaryTimeout)
		defer cancel()

		// Truncate to avoid prompt overflows (last episodicConvMaxChars covers ~4000 tokens).
		if len(conversation) > episodicConvMaxChars {
			conversation = "..." + conversation[len(conversation)-episodicConvMaxChars:]
		}

		msgs := []map[string]string{
			{
				"role":    "system",
				"content": "You are a memory archivist. Summarize the key facts and themes from the conversation in 3-5 concise sentences. Focus on factual information, not pleasantries. Do not use bullet points.",
			},
			{
				"role":    "user",
				"content": "Conversation:\n" + conversation + "\n\nWrite a 3-5 sentence episodic summary capturing what was discussed and decided:",
			},
		}

		summary, err := r.callLLM(ctx, msgs, episodicSummaryMaxTokens)
		if err != nil {
			r.logger.Debug("mem_read episodic summary: llm call failed", slog.Any("error", err))
			return
		}
		summary = strings.TrimSpace(summary)
		if summary == "" {
			return
		}

		// Embed the summary.
		vecs, err := r.embedder.Embed(ctx, []string{summary})
		if err != nil || len(vecs) == 0 {
			r.logger.Debug("mem_read episodic summary: embed failed", slog.Any("error", err))
			return
		}

		// Build node properties.
		id := uuid.New().String()
		props := map[string]any{
			"id":          id,
			"memory":      summary,
			"memory_type": episodicMemType,
			"user_name":   cubeID,
			"session_id":  sessionID,
			"status":      "activated",
			"created_at":  now,
			"updated_at":  now,
			"confidence":  0.9,
			"source":      "episodic_summarizer",
		}
		propsJSON, err := json.Marshal(props)
		if err != nil {
			return
		}

		node := db.MemoryInsertNode{
			ID:             id,
			PropertiesJSON: propsJSON,
			EmbeddingVec:   db.FormatVector(vecs[0]),
		}
		if err := r.postgres.InsertMemoryNodes(ctx, []db.MemoryInsertNode{node}); err != nil {
			r.logger.Debug("mem_read episodic summary: insert failed", slog.Any("error", err))
			return
		}
		r.logger.Debug("mem_read episodic summary: stored",
			slog.String("cube_id", cubeID),
			slog.String("session_id", sessionID),
			slog.Int("summary_len", len(summary)),
		)
	}()
}
