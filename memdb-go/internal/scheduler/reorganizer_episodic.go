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
	"github.com/anatolykoptev/memdb/memdb-go/internal/memprops"
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
//
// observationDate is the in-conversation YYYY-MM-DD anchor derived by the
// caller from the source WM rows' chat_time/observation_date (M12.1).
// Empty observationDate causes the function to skip rather than poison
// memory with a wall-clock today-leak.
func (r *Reorganizer) generateEpisodicSummary(userID, cubeID, sessionID, conversation, now, observationDate string) {
	if r.embedder == nil {
		return
	}
	if len(strings.TrimSpace(conversation)) < 100 {
		return // too short to be worth summarising
	}
	if strings.TrimSpace(observationDate) == "" {
		// M12.1: no in-conversation date plumbed → skip rather than write a
		// today-leaking row. Callers (processRawMemoryFine) compute this
		// from extractWMInfo; "" means none of the source WM rows carried
		// a chat_time, which is itself a regression to investigate.
		// TODO(phase-2): emit memdb_derived_memory_skipped_total metric.
		r.logger.Debug("mem_read episodic summary: skipped — no observation_date plumbed",
			slog.String("cube_id", cubeID), slog.String("session_id", sessionID))
		return
	}

	r.goBounded(func() {
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
			r.logger.DebugContext(ctx, "mem_read episodic summary: llm call failed", slog.Any("error", err))
			return
		}
		summary = strings.TrimSpace(summary)
		if summary == "" {
			return
		}

		// Embed the summary.
		vecs, err := r.embedder.Embed(ctx, []string{summary})
		if err != nil || len(vecs) == 0 {
			r.logger.DebugContext(ctx, "mem_read episodic summary: embed failed", slog.Any("error", err))
			return
		}

		// Build node properties via memprops — observation_date enforced.
		id := uuid.New().String()
		props, err := memprops.BuildDerivedMemoryProps(memprops.DerivedMemoryProps{
			ID:              id,
			Memory:          summary,
			MemoryType:      episodicMemType,
			UserID:          userID,
			UserName:        cubeID,
			SessionID:       sessionID,
			Now:             now,
			ObservationDate: observationDate,
			Source:          "episodic_summarizer",
			HierarchyLevel:  "episodic",
			Confidence:      0.9,
			Tags:            []string{"mode:fine", "scope:mem_read"},
		})
		if err != nil {
			r.logger.WarnContext(ctx, "mem_read episodic summary: build props failed",
				slog.String("cube_id", cubeID), slog.Any("error", err))
			return
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
			r.logger.DebugContext(ctx, "mem_read episodic summary: insert failed", slog.Any("error", err))
			return
		}
		r.logger.DebugContext(ctx, "mem_read episodic summary: stored",
			slog.String("cube_id", cubeID),
			slog.String("session_id", sessionID),
			slog.Int("summary_len", len(summary)),
		)
	})
}
