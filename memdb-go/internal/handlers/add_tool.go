package handlers

// add_tool.go — background tool trajectory extraction (fire-and-forget).
// Extracts tool call patterns and experiences from conversations containing tool usage.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/memprops"
)

const (
	toolExtractionTimeout = 90 * time.Second
)

// generateToolTrajectory asynchronously extracts tool call trajectories from the conversation.
// Fire-and-forget: errors are logged but never returned to the caller.
func (h *Handler) generateToolTrajectory(reqCtx context.Context, cubeID, userID, conversation, observationDate string) {
	if h.llmChat == nil || h.postgres == nil || h.embedder == nil {
		return
	}
	if !hasToolContent(conversation) {
		return
	}

	bgCtx := context.WithoutCancel(reqCtx)
	go func() {
		ctx, cancel := context.WithTimeout(bgCtx, toolExtractionTimeout)
		defer cancel()

		items, err := llm.ExtractToolTrajectory(ctx, h.llmChat, conversation)
		if err != nil {
			h.logger.Debug("tool trajectory extraction failed", slog.Any("error", err))
			return
		}
		if len(items) == 0 {
			return
		}

		h.logger.Debug("tool trajectory extraction: extracted",
			slog.Int("count", len(items)), slog.String("cube_id", cubeID))

		for i := range items {
			h.storeToolTrajectory(ctx, cubeID, userID, &items[i], observationDate)
		}
	}()
}

// storeToolTrajectory embeds and persists a single trajectory item.
func (h *Handler) storeToolTrajectory(ctx context.Context, cubeID, userID string, item *llm.TrajectoryItem, observationDate string) {
	// Choose text to embed: trajectory summary, or experience as fallback.
	embedText := item.Trajectory
	if embedText == "" {
		embedText = item.Experience
	}
	if embedText == "" {
		return
	}

	vecs, err := h.embedder.Embed(ctx, []string{embedText})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		h.logger.Debug("tool trajectory extraction: embed failed", slog.Any("error", err))
		return
	}

	id := uuid.New().String()
	now := nowTimestamp()

	if observationDate == "" {
		h.logger.Debug("tool trajectory extraction: skipping — empty observationDate",
			slog.String("cube_id", cubeID))
		return
	}

	props, err := buildToolTrajectoryProperties(id, cubeID, userID, now, observationDate, item)
	if err != nil {
		h.logger.Debug("tool trajectory extraction: props build failed", slog.Any("error", err))
		return
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return
	}

	embVec := db.FormatVector(vecs[0])
	node := db.MemoryInsertNode{
		ID:             id,
		PropertiesJSON: propsJSON,
		EmbeddingVec:   embVec,
	}

	if err := h.postgres.InsertMemoryNodes(ctx, []db.MemoryInsertNode{node}); err != nil {
		h.logger.Debug("tool trajectory extraction: insert failed", slog.Any("error", err))
		return
	}

	h.logger.Debug("tool trajectory extraction: stored",
		slog.String("cube_id", cubeID),
		slog.String("correctness", item.Correctness),
	)
}

// hasToolContent checks whether the conversation contains tool-related markers.
func hasToolContent(conversation string) bool {
	return strings.Contains(conversation, "tool:") ||
		strings.Contains(conversation, "[tool_calls]:") ||
		strings.Contains(conversation, "<tool_schema>")
}

// buildToolTrajectoryProperties constructs the JSONB properties for a
// ToolTrajectoryMemory node via memprops, then overlays domain-specific fields.
func buildToolTrajectoryProperties(id, cubeID, userID, now, observationDate string, item *llm.TrajectoryItem) (map[string]any, error) {
	props, err := memprops.BuildDerivedMemoryProps(memprops.DerivedMemoryProps{
		ID:              id,
		Memory:          item.Experience,
		MemoryType:      "ToolTrajectoryMemory",
		UserName:        cubeID,
		UserID:          userID,
		Now:             now,
		ObservationDate: observationDate,
		Source:          "tool_trajectory_extractor",
	})
	if err != nil {
		return nil, err
	}
	props["correctness"] = item.Correctness
	props["trajectory"] = item.Trajectory
	props["experience"] = item.Experience
	props["tool_used_status"] = item.ToolUsedStatus
	return props, nil
}
