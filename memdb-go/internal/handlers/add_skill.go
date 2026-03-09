package handlers

// add_skill.go — background skill memory extraction (fire-and-forget).
// Port of Python's process_skill_memory.py (drops OSS/MySQL/file ops).

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	skillExtractionTimeout = 90 * time.Second
	skillMinMessages       = 10
	skillCodeRatio         = 0.7
)

// generateSkillMemory asynchronously extracts skill memories from the conversation.
func (h *Handler) generateSkillMemory(cubeID, conversation string, messageCount int) {
	if h.llmChat == nil || h.postgres == nil || h.embedder == nil {
		return
	}
	if messageCount < skillMinMessages {
		return
	}
	if codeBlockRatio(conversation) > skillCodeRatio {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), skillExtractionTimeout)
		defer cancel()

		// Step 1: chunk tasks
		chunks, err := llm.ChunkTasks(ctx, h.llmChat, conversation)
		if err != nil {
			h.logger.Debug("skill extraction: chunk tasks failed", slog.Any("error", err))
			return
		}
		if len(chunks) == 0 {
			return
		}

		h.logger.Debug("skill extraction: chunked tasks",
			slog.Int("count", len(chunks)), slog.String("cube_id", cubeID))

		// Step 2: process each task chunk
		for _, chunk := range chunks {
			if chunk.Messages == "" {
				continue
			}
			h.processSkillChunk(ctx, cubeID, chunk)
		}
	}()
}

// processSkillChunk handles a single task chunk: recall → extract → store.
func (h *Handler) processSkillChunk(ctx context.Context, cubeID string, chunk llm.TaskChunk) {
	existing := h.recallExistingSkills(ctx, cubeID, chunk.TaskName)
	skill, err := llm.ExtractSkill(ctx, h.llmChat, chunk.Messages, existing)
	if err != nil {
		h.logger.Debug("skill extraction: extract failed",
			slog.String("task", chunk.TaskName), slog.Any("error", err))
		return
	}
	if skill == nil {
		return // no extractable skill
	}

	if skill.Update && skill.OldMemoryID != "" {
		if _, err := h.postgres.DeleteByPropertyIDs(ctx, []string{skill.OldMemoryID}, cubeID); err != nil {
			h.logger.Debug("skill extraction: delete old skill failed",
				slog.String("old_id", skill.OldMemoryID), slog.Any("error", err))
		}
	}

	vecs, err := h.embedder.Embed(ctx, []string{skill.Description})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		h.logger.Debug("skill extraction: embed failed", slog.Any("error", err))
		return
	}

	id := uuid.New().String()
	now := nowTimestamp()

	props := buildSkillProperties(id, cubeID, now, skill)
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
		h.logger.Debug("skill extraction: insert failed", slog.Any("error", err))
		return
	}

	h.logger.Debug("skill extraction: stored",
		slog.String("cube_id", cubeID),
		slog.String("name", skill.Name),
		slog.Bool("update", skill.Update),
	)
}

// recallExistingSkills embeds the task name and finds similar SkillMemory nodes.
func (h *Handler) recallExistingSkills(ctx context.Context, cubeID, taskName string) []llm.ExistingSkill {
	vecs, err := h.embedder.Embed(ctx, []string{taskName})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil
	}

	results, err := h.postgres.VectorSearch(ctx, vecs[0], cubeID, []string{"SkillMemory"}, "", 5)
	if err != nil {
		h.logger.Debug("skill extraction: recall failed",
			slog.String("task", taskName), slog.Any("error", err))
		return nil
	}

	existing := make([]llm.ExistingSkill, 0, len(results))
	for _, r := range results {
		es := parseExistingSkill(r.Properties)
		if es.ID != "" {
			existing = append(existing, es)
		}
	}
	return existing
}

// parseExistingSkill extracts ExistingSkill fields from a properties JSON blob.
func parseExistingSkill(propsJSON string) llm.ExistingSkill {
	var props map[string]any
	if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
		return llm.ExistingSkill{}
	}
	es := llm.ExistingSkill{
		ID:          strFromMap(props, "id"),
		Name:        strFromMap(props, "name"),
		Description: strFromMap(props, "description"),
		Procedure:   strFromMap(props, "procedure"),
	}
	if tags, ok := props["tags"].([]any); ok {
		for _, t := range tags {
			if s, ok := t.(string); ok {
				es.Tags = append(es.Tags, s)
			}
		}
	}
	return es
}

// buildSkillProperties constructs the JSONB properties for a SkillMemory node.
func buildSkillProperties(id, cubeID, now string, skill *llm.SkillMemory) map[string]any {
	props := map[string]any{
		"id":          id,
		"memory":      skill.Description,
		"memory_type": "SkillMemory",
		"user_name":   cubeID,
		"status":      "activated",
		"created_at":  now,
		"updated_at":  now,
		"confidence":  0.99,
		"source":      "skill_extractor",
		"name":        skill.Name,
		"description": skill.Description,
		"procedure":   skill.Procedure,
		"experience":  skill.Experience,
		"preference":  skill.Preference,
		"examples":    skill.Examples,
		"tags":        skill.Tags,
	}
	if skill.Scripts != nil {
		props["scripts"] = skill.Scripts
	}
	if skill.Others != nil {
		props["others"] = skill.Others
	}
	return props
}

// strFromMap extracts a string value from a map, returning "" if absent or wrong type.
func strFromMap(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
