// Package search — tests for the per-stage telemetry helpers added by the
// forensic 2026-05-01 telemetry-gap fix. These cover the no-panic /
// no-overhead-on-zero contracts; full series shape is exercised by the
// pre-registration block in searchMx() and the m12_smoke_test.go scrape
// assertion in the observability package.
package search

import (
	"context"
	"testing"
)

// TestRecordHelpers_NoPanicOnZeros — every helper must short-circuit cleanly
// when called with a zero / empty payload. These call sites land on the hot
// path (per-search, per-chat) so the cheap "do nothing" branch is what we
// actually rely on in production.
func TestRecordHelpers_NoPanicOnZeros(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// dedupDrop with n<=0 is a no-op.
	recordDedupDrop(ctx, "exact_text", 0)
	recordDedupDrop(ctx, "exact_text", -3)

	// thresholdFilter with both kept and dropped == 0 is a no-op.
	RecordThresholdFilter(ctx, 0, 0, false)
	RecordThresholdFilter(ctx, 0, 0, true)

	// rerankDuration with negative seconds is a no-op (defensive).
	recordRerankDuration(ctx, "cosine", -1.0)

	// retrieveDuration with empty cube_id is allowed (multi-cube path).
	recordRetrieveDuration(ctx, "", "other", 0.001)

	// llmChatDuration with empty model is allowed (degraded telemetry).
	RecordLLMChatDuration(ctx, "", 0.001)

	// contextTruncated takes no args beyond ctx — bumps by 1.
	RecordContextTruncated(ctx)
}

// TestRecordHelpers_HappyPath — exercise the real recording paths so the
// instruments are forced through their first lazy-init call. Verifies no
// panic when every counter / histogram receives a real value.
func TestRecordHelpers_HappyPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	recordDedupDrop(ctx, "sim_threshold", 5)
	recordDedupDrop(ctx, "mmr_high_sim", 2)
	recordDedupDrop(ctx, "exact_text", 1)
	recordDedupDrop(ctx, "cross_source", 3)
	recordDedupDrop(ctx, "threshold_filter", 7)

	RecordThresholdFilter(ctx, 3, 2, false)
	RecordThresholdFilter(ctx, 1, 9, true)

	for _, strat := range []string{"cosine", "cross_encoder", "llm_judge", "mmr", "staged"} {
		recordRerankDuration(ctx, strat, 0.123)
	}
	recordRetrieveDuration(ctx, "cube-A", "cat2", 0.05)
	RecordLLMChatDuration(ctx, "claude-sonnet-4-6", 1.2)
	RecordContextTruncated(ctx)
}

// TestSearchMx_InstrumentsRegistered — the new instruments MUST be non-nil
// after the first searchMx() call. A nil instrument here would silently drop
// every observation in production.
func TestSearchMx_InstrumentsRegistered(t *testing.T) {
	t.Parallel()
	mx := searchMx()
	if mx.RetrieveDuration == nil {
		t.Error("RetrieveDuration is nil — pre-stage retrieve histogram unregistered")
	}
	if mx.RerankDuration == nil {
		t.Error("RerankDuration is nil — per-strategy rerank histogram unregistered")
	}
	if mx.LLMChatDuration == nil {
		t.Error("LLMChatDuration is nil — chat-LLM histogram unregistered")
	}
	if mx.DedupDrop == nil {
		t.Error("DedupDrop is nil — dedup-drop counter unregistered")
	}
	if mx.ContextTruncated == nil {
		t.Error("ContextTruncated is nil — formatMemories truncation counter unregistered")
	}
	if mx.ThresholdFilterKept == nil || mx.ThresholdFilterDropped == nil {
		t.Error("ThresholdFilter{Kept,Dropped} is nil — per-item filter counters unregistered")
	}
}
