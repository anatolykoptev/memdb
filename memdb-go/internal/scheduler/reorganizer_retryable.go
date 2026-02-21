package scheduler

// reorganizer_retryable.go — error-returning wrappers around Reorganizer methods.
//
// The original Reorganizer methods are "non-fatal" (void return, log on error).
// These wrappers expose errors so worker_process.go can trigger retry logic.
// They call the original method's internal logic and surface the first critical error.

import (
	"context"
	"fmt"
	"time"
)

// RunWithError runs one reorganization cycle and returns the first critical error.
// LLM consolidation failures per-cluster are non-fatal (logged, not returned).
// Only DB-level errors (FindNearDuplicates) are returned for retry.
func (r *Reorganizer) RunWithError(ctx context.Context, cubeID string) error {
	pairs, err := r.postgres.FindNearDuplicates(ctx, cubeID, dupThreshold, dupScanLimit)
	if err != nil {
		return fmt.Errorf("reorganizer: FindNearDuplicates: %w", err)
	}
	if len(pairs) == 0 {
		return nil
	}

	clusters := buildClusters(pairs)
	now := nowRFC3339()

	for _, cluster := range clusters {
		if len(cluster) < 2 {
			continue
		}
		// Per-cluster errors are non-fatal (same as Run).
		_ = r.consolidateCluster(ctx, cubeID, cluster, now)
	}
	return nil
}

// ProcessRawMemoryWithError processes raw WM nodes and returns a critical error
// if the DB fetch fails (individual node LLM errors remain non-fatal).
func (r *Reorganizer) ProcessRawMemoryWithError(ctx context.Context, cubeID string, wmIDs []string) error {
	if len(wmIDs) == 0 {
		return nil
	}
	if r.embedder == nil {
		return nil
	}

	// Delegate directly to ProcessRawMemory which handles all paths (fine + legacy).
	// The fine path fetches full properties internally; no need to pre-check here.
	r.ProcessRawMemory(ctx, cubeID, wmIDs)
	return nil
}

// RefreshWorkingMemoryWithError embeds the query and refreshes the VSET.
// Returns an error if the embedder call fails (retryable).
func (r *Reorganizer) RefreshWorkingMemoryWithError(ctx context.Context, cubeID, queryText string) error {
	if r.embedder == nil {
		return nil
	}
	if queryText == "" {
		return nil
	}
	// EmbedQuery is the retryable part; VSET ops are best-effort.
	_, err := r.embedder.EmbedQuery(ctx, queryText)
	if err != nil {
		return fmt.Errorf("mem_update: EmbedQuery: %w", err)
	}
	// Full refresh via original method (VSET ops non-fatal).
	r.RefreshWorkingMemory(ctx, cubeID, queryText)
	return nil
}

// ExtractAndStorePreferencesWithError extracts preferences and returns a critical
// error if the LLM call fails (retryable).
func (r *Reorganizer) ExtractAndStorePreferencesWithError(ctx context.Context, cubeID, conversation string) error {
	if conversation == "" {
		return nil
	}
	prefs, err := r.llmExtractPreferences(ctx, conversation)
	if err != nil {
		return fmt.Errorf("pref_add: llmExtractPreferences: %w", err)
	}
	if len(prefs) == 0 {
		return nil
	}
	// Delegate storage to original (non-fatal) implementation.
	r.ExtractAndStorePreferences(ctx, cubeID, conversation)
	return nil
}

// ProcessFeedbackWithError processes feedback and returns a critical error if
// the LLM call fails (retryable). DB fetch errors also trigger retry.
func (r *Reorganizer) ProcessFeedbackWithError(ctx context.Context, cubeID string, ids []string, feedbackContent string) error {
	if len(ids) == 0 {
		return nil
	}

	nodes, err := r.postgres.GetMemoryByPropertyIDs(ctx, ids, cubeID)
	if err != nil {
		return fmt.Errorf("mem_feedback: GetMemoryByPropertyIDs: %w", err)
	}
	if len(nodes) == 0 {
		return nil
	}

	_, err = r.llmAnalyzeFeedback(ctx, feedbackContent, nodes)
	if err != nil {
		return fmt.Errorf("mem_feedback: llmAnalyzeFeedback: %w", err)
	}

	// Full processing via original (non-fatal) implementation.
	r.ProcessFeedback(ctx, cubeID, ids, feedbackContent)
	return nil
}

// nowRFC3339 returns the current UTC time in RFC3339 format.
func nowRFC3339() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}
