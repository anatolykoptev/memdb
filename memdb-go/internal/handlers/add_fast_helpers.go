package handlers

// add_fast_helpers.go — helper functions for the fast-mode add pipeline.
// Covers: hash computation, embedding, cosine dedup, VSET cache writes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// computeHashes returns content-hash for each memory.
func computeHashes(memories []extractedMemory) []string {
	hashes := make([]string, len(memories))
	for i, mem := range memories {
		hashes[i] = textHash(mem.Text)
	}
	return hashes
}

// filterExistingHashes returns the set of content hashes already in the DB.
// Returns nil on error (caller continues without hash dedup).
func (h *Handler) filterExistingHashes(ctx context.Context, hashes []string, cubeID string) map[string]bool {
	existing, err := h.postgres.FilterExistingContentHashes(ctx, hashes, cubeID)
	if err != nil {
		h.logger.Debug("fast add: batch hash check failed (continuing without hash dedup)",
			slog.Any("error", err))
		return nil
	}
	return existing
}

// mergeInfo creates a new info map with content_hash added.
func mergeInfo(base map[string]any, hash string) map[string]any {
	merged := make(map[string]any, len(base)+1)
	for k, v := range base {
		merged[k] = v
	}
	merged["content_hash"] = hash
	return merged
}

// embedSingle embeds a single text, returning the embedding vector.
func (h *Handler) embedSingle(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := h.embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(embeddings) == 0 || len(embeddings[0]) == 0 {
		return nil, errors.New("embedder returned empty result")
	}
	return embeddings[0], nil
}

// isDuplicate returns true if a near-duplicate already exists in the DB.
//
// Session-id aware: when sessionID != "" and the candidate match comes
// from a different session_id, the match is rejected — multi-session
// conversational data (LoCoMo, vaelor multi-turn, oxpulse multi-day)
// frequently produces cosine ≥ dedupThreshold across sessions just
// because the same speakers chat about overlapping topics. Treating
// those as duplicates silently dropped 5/6 LoCoMo conv-26 sessions
// from /product/add (verified empirically — see fix/fast-cross-session-dedup).
//
// Threshold is env-tunable via MEMDB_FAST_DEDUP_THRESHOLD; default
// preserves the historical 0.92 for back-compat. Operators that want
// the legacy "drop similar facts even across sessions" behaviour can
// keep MEMDB_FAST_DEDUP_SESSION_AWARE=0.
func (h *Handler) isDuplicate(ctx context.Context, embedding []float32, cubeID, agentID, sessionID string) bool {
	searchTypes := []string{"LongTermMemory", "UserMemory", "WorkingMemory"}
	threshold := fastDedupThreshold()
	// Fetch top-3 (not top-1) so we can skip cross-session matches and
	// still consider the next-best candidate within the same session.
	results, err := h.postgres.VectorSearch(ctx, embedding, cubeID, cubeID, searchTypes, agentID, 3)
	if err != nil {
		h.logger.Debug("dedup vector search failed", slog.Any("error", err))
		return false
	}
	sessionAware := sessionID != "" && fastDedupSessionAware()
	for _, r := range results {
		if r.Score < threshold {
			break // sorted desc; remaining are below threshold
		}
		matchSession := extractSessionID(r.Properties)
		sameSession := matchSession == sessionID
		if sessionAware && !sameSession {
			// Cross-session match — record as "near-miss" so dashboards
			// surface threshold drift over time, but DO NOT drop.
			addMx().DuplicateDrop.Add(ctx, 1, metric.WithAttributes(
				attribute.String("outcome", "cross_session_skip"),
				attribute.Bool("same_session", false),
			))
			continue
		}
		addMx().DuplicateDrop.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", "dropped"),
			attribute.Bool("same_session", sameSession),
		))
		h.logger.Debug("skipping duplicate by cosine similarity",
			slog.Float64("score", r.Score),
			slog.String("session_id", sessionID),
			slog.Bool("same_session", sameSession))
		return true
	}
	return false
}

// extractSessionID pulls properties.session_id out of the raw JSON
// blob returned by VectorSearch. Returns "" on any parse error or
// missing field — caller treats "" as "session unknown" and the
// session-aware filter falls back to non-restrictive behaviour.
func extractSessionID(props string) string {
	if props == "" {
		return ""
	}
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(props), &p); err != nil {
		return ""
	}
	return p.SessionID
}

// fastDedupThreshold reads MEMDB_FAST_DEDUP_THRESHOLD; default = 0.92
// (back-compat with the pre-fix constant). Out-of-range values are
// clamped to (0, 1) and fall back to the default.
func fastDedupThreshold() float64 {
	raw := strings.TrimSpace(os.Getenv("MEMDB_FAST_DEDUP_THRESHOLD"))
	if raw == "" {
		return dedupThreshold
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || v > 1 {
		return dedupThreshold
	}
	return v
}

// fastDedupSessionAware reads MEMDB_FAST_DEDUP_SESSION_AWARE; default = true.
// "0" / "false" / "off" disables and restores the legacy "match any
// session" dedup.
func fastDedupSessionAware() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MEMDB_FAST_DEDUP_SESSION_AWARE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// writeWMCache writes accepted WorkingMemory nodes to the VSET hot cache (non-fatal).
// allNodes layout: [WM0, LTM0, WM1, LTM1, ...] — WM at even indices.
func (h *Handler) writeWMCache(ctx context.Context, cubeID string, allNodes []db.MemoryInsertNode, items []addResponseItem, wmEmbeddings [][]float32) {
	if h.wmCache == nil {
		return
	}
	ts := nowUnix()
	for i := 0; i+1 < len(allNodes); i += 2 {
		wm := allNodes[i]
		itemIdx := i / 2
		if itemIdx >= len(items) {
			break
		}
		if itemIdx < len(wmEmbeddings) && len(wmEmbeddings[itemIdx]) > 0 {
			if err := h.wmCache.VAdd(ctx, cubeID, wm.ID, items[itemIdx].Memory, wmEmbeddings[itemIdx], ts); err != nil {
				h.logger.Debug("fast add: vset write failed", slog.String("id", wm.ID), slog.Any("error", err))
			}
		}
	}
}
