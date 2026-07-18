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
	"github.com/anatolykoptev/memdb/memdb-go/internal/memprops"
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
	systemPrompt, memoryType := tierPromptFor(ctx, targetLevel)

	// Build the user payload — {id, text} so the LLM sees each child as a source.
	type inputItem struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	items := make([]inputItem, len(cluster))
	for i, n := range cluster {
		items[i] = inputItem{ID: n.ID, Text: n.Text}
	}
	payload, err := json.Marshal(items)
	if err != nil {
		// inputItem is always marshalable; this is a defensive guard.
		r.logger.WarnContext(ctx, "tree_summariser: marshal input items failed",
			slog.Any("error", err))
		return tierParentResult{}, fmt.Errorf("marshal cluster items: %w", err)
	}
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

	// M12.1: derive the parent's observation_date as max over the cluster
	// children. The parent represents "what the cube knew by date X" — using
	// max anchors retrieval to the latest in-conversation moment the
	// cluster covers. Using min would push the parent backward through
	// long-tail history; using created_at would resurrect the today-leak
	// (this exact bug — 2219 tree_reorganizer rows globally with
	// observation_date absent — see PR #299 forensic SQL).
	clusterObsDate := ""
	for _, n := range cluster {
		d := n.ObservationDate
		if len(d) >= 10 {
			d = d[:10]
		}
		if d > clusterObsDate {
			clusterObsDate = d
		}
	}
	if clusterObsDate == "" {
		// Defence-in-depth: skip rather than poison memory with NOW. The
		// upstream SQL projects observation_date COALESCE'd with chat_time,
		// so empty here means EVERY child row was missing both — a
		// regression worth tracking but not worth materialising into a
		// today-leaking tree parent.
		// TODO(phase-2): emit memdb_tree_reorg_skipped_total{reason="no_observation_date"}.
		r.logger.WarnContext(ctx, "tree reorg: skipping tier parent — no observation_date on any child",
			slog.String("cube_id", cubeID), slog.String("tier", targetLevel),
			slog.Int("children", len(cluster)))
		return tierParentResult{PromptSHA: promptSHA}, nil
	}

	parentID := uuid.New().String()
	props, err := memprops.BuildDerivedMemoryProps(memprops.DerivedMemoryProps{
		ID:              parentID,
		Memory:          summary,
		MemoryType:      memoryType,
		UserID:          userID,
		UserName:        cubeID,
		Now:             now,
		ObservationDate: clusterObsDate,
		Source:          "tree_reorganizer",
		HierarchyLevel:  targetLevel,
		Confidence:      0.9,
		Tags:            []string{"mode:fine", "tier:" + targetLevel},
	})
	if err != nil {
		return tierParentResult{PromptSHA: promptSHA}, fmt.Errorf("build tier parent props: %w", err)
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
	r.logger.DebugContext(ctx, "tree reorg: tier parent created",
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
func tierPromptFor(ctx context.Context, level string) (string, string) {
	switch level {
	case hierarchyLevelSemantic:
		return schedulerPrompt(ctx, "semantic-tier-abstractor"), memoryTypeSemantic
	default:
		return schedulerPrompt(ctx, "episodic-tier-archivist"), memoryTypeEpisodic
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
