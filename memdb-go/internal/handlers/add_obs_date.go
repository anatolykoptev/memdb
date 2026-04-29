// Package handlers — add_obs_date.go: F11 observation_date coverage lift.
//
// observationDateFromMessages only fires when callers stamp chat_time on each
// message. Many callers don't, leaving observation_date empty on 87%+ of rows.
//
// This file adds resolveObservationDate: a thin wrapper around
// observationDateFromMessages that falls back to an LLM-based extractor when
// chat_time is absent from every message and MEMDB_OBS_DATE_EXTRACT=true is set.
//
// The LLM extractor (llm.ObsDateExtractor) is lazy-cached on the same pattern
// as getAtomicExtractor: re-allocated only when the underlying client pointer
// changes, zero overhead when the feature is off.
package handlers

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const obsDateExtractEnvVar = "MEMDB_OBS_DATE_EXTRACT"

// obsDateEnabled returns true when MEMDB_OBS_DATE_EXTRACT is set to a truthy
// value. Default off — same staged-rollout pattern as atomicFactsEnabled.
func obsDateEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(obsDateExtractEnvVar)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// obsDateExtractorCacheT is the lazy singleton for the obs_date extractor.
type obsDateExtractorCacheT struct {
	mu      sync.Mutex
	client  *llm.Client
	wrapper *llm.ObsDateExtractor
}

var obsDateExtractorCache obsDateExtractorCacheT //nolint:gochecknoglobals // cache singleton

// getObsDateExtractor returns the cached ObsDateExtractor for the handler's
// configured LLM client. Re-allocates if the underlying client pointer changes.
// Returns nil when no LLM extractor is configured.
func (h *Handler) getObsDateExtractor() *llm.ObsDateExtractor {
	if h.llmExtractor == nil {
		return nil
	}
	c := h.llmExtractor.Client()
	obsDateExtractorCache.mu.Lock()
	defer obsDateExtractorCache.mu.Unlock()
	if obsDateExtractorCache.wrapper == nil || obsDateExtractorCache.client != c {
		obsDateExtractorCache.client = c
		obsDateExtractorCache.wrapper = llm.NewObsDateExtractor(c)
	}
	return obsDateExtractorCache.wrapper
}

// resolveObservationDate returns the in-conversation date for a set of messages.
//
// Primary path: read chat_time from the latest message (same as
// observationDateFromMessages). Secondary path (MEMDB_OBS_DATE_EXTRACT=true
// AND primary returned ""): call the LLM extractor to infer the date from
// message text, using today's UTC date as the resolution anchor.
//
// Result is always YYYY-MM-DD or "" — callers treat "" as "no timestamp known".
func (h *Handler) resolveObservationDate(ctx context.Context, messages []chatMessage) string {
	// Fast path: message metadata carries chat_time.
	if d := observationDateFromMessages(messages); d != "" {
		return d
	}

	// Slow path: LLM inference (env-gated).
	if !obsDateEnabled() {
		return ""
	}
	ext := h.getObsDateExtractor()
	if ext == nil {
		return ""
	}

	texts := make([]string, 0, len(messages))
	for _, m := range messages {
		if t := strings.TrimSpace(m.Content); t != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) == 0 {
		return ""
	}

	// referenceDate is today — the LLM uses it to resolve relative expressions.
	// We intentionally do NOT pass observationDate here because it's empty by
	// definition (that's why we're in the slow path).
	d, err := ext.ExtractObservationDate(ctx, texts, "")
	if err != nil {
		// Soft failure: log is too noisy for a background fallback; the metric
		// memdb_extract_date_confidence_dropped_total fires if LLM returned a
		// non-ISO string. Transport errors are swallowed intentionally.
		return ""
	}
	return d
}
