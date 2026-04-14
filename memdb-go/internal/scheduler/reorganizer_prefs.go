package scheduler

// reorganizer_prefs.go — pref_add handler: LLM preference extraction and storage.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	// prefDedupThreshold is the cosine similarity above which a new preference
	// is considered a near-duplicate of an existing one and skipped.
	prefDedupThreshold = 0.93
	// prefDedupTopK is how many existing prefs we retrieve per new pref for dedup check.
	prefDedupTopK = 3
)

// ExtractAndStorePreferences implements the Go-native pref_add handler.
//
// Extracts user preferences from conversation text via LLM and stores them
// as UserMemory nodes in Postgres (same retrieval pipeline as LTM/UserMemory).
// This replaces Python's pref_mem service — no Qdrant dependency required.
//
// Non-fatal: errors are logged; the method always returns normally.
func (r *Reorganizer) ExtractAndStorePreferences(ctx context.Context, userID, cubeID, conversation string) {
	if conversation == "" {
		return
	}
	if r.embedder == nil {
		r.logger.Debug("pref_add: embedder not configured, skipping")
		return
	}

	log := r.logger.With(slog.String("cube_id", cubeID))

	// Step 1: LLM extraction.
	prefs, err := r.llmExtractPreferences(ctx, conversation)
	if err != nil {
		log.Warn("pref_add: llm extraction failed", slog.Any("error", err))
		return
	}
	if len(prefs) == 0 {
		log.Debug("pref_add: no preferences extracted")
		return
	}
	log.Info("pref_add: preferences extracted", slog.Int("count", len(prefs)))

	// Step 2: embed all preference texts in one batch.
	embs, err := r.embedder.Embed(ctx, prefs)
	if err != nil {
		log.Warn("pref_add: embed failed", slog.Any("error", err))
		return
	}

	// Step 2b: dedup — skip preferences that are near-duplicates of existing ones.
	prefs, embs = r.dedupPreferences(ctx, cubeID, prefs, embs, log)
	if len(prefs) == 0 {
		log.Debug("pref_add: all preferences already exist (dedup)")
		return
	}

	// Step 3: build and insert UserMemory nodes.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var nodes []db.MemoryInsertNode
	for i, text := range prefs {
		if i >= len(embs) || len(embs[i]) == 0 {
			continue
		}
		id := uuid.New().String()
		props := map[string]any{
			"id":               id,
			"memory":           text,
			"memory_type":      "UserMemory",
			"user_name":        cubeID, // partition key (upstream convention)
			"user_id":          userID, // person identity — Phase 2 split
			"status":           "activated",
			"created_at":       now,
			"updated_at":       now,
			"tags":             []string{"mode:pref_add"},
			"delete_time":      "",
			"delete_record_id": "",
		}
		propsJSON, _ := json.Marshal(props)
		nodes = append(nodes, db.MemoryInsertNode{
			ID:             id,
			PropertiesJSON: propsJSON,
			EmbeddingVec:   db.FormatVector(embs[i]),
		})
	}
	if len(nodes) == 0 {
		return
	}
	if err := r.postgres.InsertMemoryNodes(ctx, nodes); err != nil {
		log.Warn("pref_add: insert failed", slog.Any("error", err))
		return
	}
	log.Info("pref_add: preferences stored", slog.Int("inserted", len(nodes)))
}

// dedupPreferences filters out preference texts whose embedding is within
// prefDedupThreshold of an already-stored UserMemory in the cube.
// Returns filtered (prefs, embs) slices with only genuinely new entries.
func (r *Reorganizer) dedupPreferences(ctx context.Context, cubeID string, prefs []string, embs [][]float32, log *slog.Logger) ([]string, [][]float32) {
	var kept []string
	var keptEmbs [][]float32
	for i, emb := range embs {
		if len(emb) == 0 {
			continue
		}
		results, err := r.postgres.VectorSearch(ctx, emb, cubeID, "", []string{"UserMemory"}, "", prefDedupTopK)
		if err != nil {
			// On error keep the pref (safe default)
			kept = append(kept, prefs[i])
			keptEmbs = append(keptEmbs, emb)
			continue
		}
		isDup := false
		for _, res := range results {
			if res.Score >= prefDedupThreshold {
				log.Debug("pref_add: skipping duplicate preference",
					slog.String("text", prefs[i]),
					slog.Float64("score", res.Score),
				)
				isDup = true
				break
			}
		}
		if !isDup {
			kept = append(kept, prefs[i])
			keptEmbs = append(keptEmbs, emb)
		}
	}
	return kept, keptEmbs
}

// llmExtractPreferences calls the LLM to extract user preferences from a conversation.
func (r *Reorganizer) llmExtractPreferences(ctx context.Context, conversation string) ([]string, error) {
	msgs := []map[string]string{
		{"role": "system", "content": prefExtractionSystemPrompt},
		{"role": "user", "content": "Conversation:\n" + conversation},
	}

	callCtx, cancel := context.WithTimeout(ctx, reorganizerLLMTimeout)
	defer cancel()

	raw, err := r.callLLM(callCtx, msgs, llmCompactMaxTokens)
	if err != nil {
		return nil, err
	}

	raw = stripFences(raw)
	var result struct {
		Preferences []struct {
			Text string `json:"text"`
		} `json:"preferences"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("pref extract parse json (%s): %w", truncate(raw, llmTruncateLen), err)
	}
	out := make([]string, 0, len(result.Preferences))
	for _, p := range result.Preferences {
		if p.Text != "" {
			out = append(out, p.Text)
		}
	}
	return out, nil
}
