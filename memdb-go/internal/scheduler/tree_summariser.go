package scheduler

// tree_summariser.go — LLM tier summarisation + parent-node persistence for D3.
//
// Port of Python `tree_text_memory/organize/reorganizer.py`'s `_summarize_cluster`.
// Separate from tree_reorganizer.go (pure clustering) because this file owns
// LLM I/O, embedding, and the DB write — distinct concern, distinct failure
// modes. Also keeps each file ≤200 lines per repo policy.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// tierParentResult carries everything the caller needs about a freshly-created
// tier parent. Kept as a named struct (rather than a pile of positional
// returns) because callers pass these fields directly into the relation phase.
type tierParentResult struct {
	ParentID  string
	PromptSHA string
	Summary   string
	Embedding []float32
}

// createTierParent calls the LLM to summarise the cluster, embeds the summary,
// and inserts the resulting node as an Episodic/SemanticMemory. Returns the
// new parent's UUID, the sha256 of the LLM prompt (for audit-log diagnostics),
// the summary text itself (for downstream relation detection), and the raw
// summary embedding so callers can reuse it for relation detection without
// re-embedding.
//
// Empty summary → empty ParentID returned and no DB write; caller treats this
// as a no-op (not a failure). Matches the Python manager.py convention of
// dropping clusters whose summariser returns "".
func (r *Reorganizer) createTierParent(ctx context.Context, cubeID string, cluster []hierarchyNode, targetLevel, now string) (tierParentResult, error) {
	systemPrompt, memoryType := tierPromptFor(targetLevel)

	// Build the user payload — {id, text} so the LLM sees each child as a source.
	type inputItem struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	items := make([]inputItem, len(cluster))
	for i, n := range cluster {
		items[i] = inputItem{ID: n.ID, Text: n.Text}
	}
	payload, _ := json.Marshal(items)
	userMsg := "Memory cluster to summarise:\n" + string(payload)

	promptSHA := sha256Hex(systemPrompt + "\n---\n" + userMsg)

	callCtx, cancel := context.WithTimeout(ctx, tierSummaryTimeout)
	defer cancel()

	raw, err := r.callLLM(callCtx, []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": userMsg},
	}, tierSummaryMaxTokens)
	if err != nil {
		return tierParentResult{PromptSHA: promptSHA}, fmt.Errorf("tier summarise llm: %w", err)
	}
	raw = string(llm.StripJSONFence([]byte(raw)))

	var parsed struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return tierParentResult{PromptSHA: promptSHA}, fmt.Errorf("parse tier summary json (%s): %w", truncate(raw, 200), err)
	}
	summary := strings.TrimSpace(parsed.Summary)
	if summary == "" {
		return tierParentResult{PromptSHA: promptSHA}, nil
	}

	return r.persistTierParent(ctx, cubeID, cluster, targetLevel, memoryType, summary, promptSHA, now)
}

// persistTierParent embeds the summary, marshals properties, and writes the
// new parent node. Split out of createTierParent to keep the LLM-call path
// and the DB-write path separately testable / mockable. Returns the raw
// embedding alongside parentID so upstream callers can reuse it.
func (r *Reorganizer) persistTierParent(ctx context.Context, cubeID string, cluster []hierarchyNode, targetLevel, memoryType, summary, promptSHA, now string) (tierParentResult, error) {
	embVec := ""
	var embRaw []float32
	userID := ""
	if len(cluster) > 0 {
		userID = cluster[0].UserID
	}
	if r.embedder != nil {
		embs, err := r.embedder.Embed(ctx, []string{summary})
		if err == nil && len(embs) > 0 && len(embs[0]) > 0 {
			embRaw = embs[0]
			embVec = db.FormatVector(embs[0])
		}
	}

	parentID := uuid.New().String()
	props := map[string]any{
		"id":               parentID,
		"memory":           summary,
		"memory_type":      memoryType,
		"user_name":        cubeID,
		"user_id":          userID,
		"status":           "activated",
		"created_at":       now,
		"updated_at":       now,
		"confidence":       0.9,
		"source":           "tree_reorganizer",
		"hierarchy_level":  targetLevel,
		"parent_memory_id": nil,
		"tags":             []string{"mode:fine", "tier:" + targetLevel},
	}
	propsJSON, err := json.Marshal(props)
	if err != nil {
		return tierParentResult{PromptSHA: promptSHA}, fmt.Errorf("marshal tier parent props: %w", err)
	}

	if err := r.postgres.InsertMemoryNodes(ctx, []db.MemoryInsertNode{{
		ID:             parentID,
		PropertiesJSON: propsJSON,
		EmbeddingVec:   embVec,
	}}); err != nil {
		return tierParentResult{PromptSHA: promptSHA}, fmt.Errorf("insert tier parent: %w", err)
	}
	r.logger.Debug("tree reorg: tier parent created",
		slog.String("cube_id", cubeID),
		slog.String("tier", targetLevel),
		slog.String("parent_id", parentID),
		slog.Int("children", len(cluster)),
	)
	return tierParentResult{
		ParentID:  parentID,
		PromptSHA: promptSHA,
		Summary:   summary,
		Embedding: embRaw,
	}, nil
}

// tierPromptFor returns (system prompt, memory_type) for a given target tier.
func tierPromptFor(level string) (string, string) {
	switch level {
	case hierarchyLevelSemantic:
		return semanticTierSystemPrompt, memoryTypeSemantic
	default:
		return episodicTierSystemPrompt, memoryTypeEpisodic
	}
}

// sha256Hex returns hex(sha256(s)).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// newUUID is a thin wrapper so tests can stub out UUID generation (future).
func newUUID() string {
	return uuid.New().String()
}
