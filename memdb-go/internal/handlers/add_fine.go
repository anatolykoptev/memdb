package handlers

// add_fine.go — fine-mode memory add orchestrator (LLM-based).
// Pipeline: classify → fetch candidates → extract+dedup → embed → apply →
// fan-out background extractors. Each step lives in its own file
// (add_fine_candidates.go / add_fine_actions.go / add_fine_async.go); this
// file only sequences them.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// nativeFineAddForCube implements the fine-mode add pipeline (v2) for a single cube.
// Steps emit per-stage timing into memdb.add.stage_duration_ms{stage=...}.
//
// M11 F8: when MEMDB_ATOMIC_FACTS=true, delegates to runAtomicFineForCube,
// which uses mem0's verbatim ADDITIVE_EXTRACTION_PROMPT (atomic per-fact
// extraction). Default-off preserves the legacy single-paragraph path
// byte-identical.
//
// Resilience: any LLM timeout / unavailable error from the extraction stage
// degrades the request to fast-mode (embed-only) instead of returning a 503.
// Caller still gets memory rows — they just lack atomic / linked / event_dates
// metadata. Counted into memdb_add_fine_fallback_total{reason}.
func (h *Handler) nativeFineAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	if atomicFactsEnabled() {
		items, err := h.runAtomicFineForCube(ctx, req, cubeID)
		if err == nil || !shouldFallbackToFast(err) {
			return items, err
		}
		recordFineFallback(ctx, fineFallbackReason(err))
		h.logger.Warn("fine add: atomic extractor failed, falling back to fast",
			slog.String("cube_id", cubeID),
			slog.Any("error", err))
		return h.nativeFastAddForCube(ctx, req, cubeID)
	}
	if len(req.Messages) == 0 {
		return nil, nil
	}
	// Prefer per-message chat_time over wall-clock so D6 / F3 / episodic
	// resolve "yesterday" relative to the conversation, not the ingest hour.
	now := conversationNowAnchor(req.Messages)
	sessionID := stringOrEmpty(req.SessionID)
	conversation := formatConversation(req.Messages, now)

	// Stage 1: classify — early-exit on skip.
	t := time.Now()
	sig := classifyContent(req.Messages, conversation)
	recordStageDuration(ctx, "classify", t)
	if sig.Skip {
		h.logger.Debug("fine add: skipped extraction",
			slog.String("reason", sig.SkipReason), slog.String("cube_id", cubeID))
		return nil, nil
	}

	// Stage 2: candidate fetch + LLM extract+dedup + hallucination filter.
	t = time.Now()
	facts, candOK, err := h.runFineExtraction(ctx, conversation, cubeID, req, &sig)
	recordStageDuration(ctx, "extract", t)
	if err != nil {
		if shouldFallbackToFast(err) {
			recordFineFallback(ctx, fineFallbackReason(err))
			h.logger.Warn("fine add: legacy extractor failed, falling back to fast",
				slog.String("cube_id", cubeID),
				slog.Any("error", err))
			return h.nativeFastAddForCube(ctx, req, cubeID)
		}
		return nil, err
	}
	if !candOK || len(facts) == 0 {
		return nil, nil
	}

	// Stage 3: embed ADD/UPDATE facts (single batched ONNX call).
	t = time.Now()
	facts = h.filterAddsByContentHash(ctx, facts, cubeID)
	embedded := h.embedFacts(ctx, facts)
	recordStageDuration(ctx, "embed", t)

	// Stage 4: apply actions + persist + entity links + WM cleanup.
	t = time.Now()
	fc := factContext{
		CubeID: cubeID, UserID: *req.UserID, AgentID: stringOrEmpty(req.AgentID),
		SessionID: sessionID, Now: now, Info: mapOrEmpty(req.Info),
		CustomTags:      req.CustomTags,
		Sources:         buildSourcesFromMessages(req.Messages),
		Key:             stringOrEmpty(req.Key),
		ObservationDate: h.resolveObservationDate(ctx, req.Messages),
	}
	items, err := h.applyAndPersistFineFacts(ctx, embedded, fc)
	recordStageDuration(ctx, "apply", t)
	if err != nil {
		return nil, err
	}

	// Stage 5: fire-and-forget background extractors (5 today, +F3 soon).
	t = time.Now()
	h.triggerBackgroundExtractors(extractorTriggerInput{
		ReqCtx: ctx,
		CubeID: cubeID, UserID: *req.UserID, SessionID: sessionID,
		Conversation: conversation, Now: now,
		FactCount: len(facts), MessageCount: len(req.Messages),
	})
	recordStageDuration(ctx, "fanout", t)
	return items, nil
}

// shouldFallbackToFast returns true for LLM-availability errors that fast can
// still serve through (embed-only). Hard contract violations (validation,
// invalid request shape) propagate unchanged so callers see the real bug.
//
// Detection ladder:
//
//  1. context.DeadlineExceeded — timeout from llm.WithTimeout (60s atomic cap)
//  2. context.Canceled — client cancelled request mid-extraction
//  3. wrapped error containing "context deadline exceeded" — pgx/rerank
//     drivers wrap before bubbling up; std errors.Is may miss those
//  4. wrapped error containing "circuit breaker open" — go-kit CircuitBreaker
//     trip on cascading LLM failures
//
// Anything else (parse error, dim mismatch, sql constraint) → no fallback.
func shouldFallbackToFast(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "context deadline exceeded"):
		return true
	case strings.Contains(msg, "circuit breaker open"):
		return true
	case strings.Contains(msg, "circuit_open"):
		return true
	}
	return false
}

// fineFallbackReason maps the trigger error onto a short metric label.
// Keep label cardinality bounded — three reasons cover every fallback path.
func fineFallbackReason(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "llm_timeout"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "circuit"):
		return "circuit_open"
	case strings.Contains(msg, "context deadline exceeded"):
		return "llm_timeout"
	}
	return "llm_error"
}

// recordFineFallback bumps memdb_add_fine_fallback_total{reason}. Pre-registered
// for {llm_timeout, circuit_open, canceled, llm_error, unknown} in metrics_add.go.
func recordFineFallback(ctx context.Context, reason string) {
	mx := addMx()
	if mx == nil || mx.FineFallback == nil {
		return
	}
	mx.FineFallback.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}
