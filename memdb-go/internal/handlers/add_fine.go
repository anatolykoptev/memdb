package handlers

// add_fine.go — fine-mode memory add orchestrator (LLM-based).
// Pipeline: classify → fetch candidates → extract+dedup → embed → apply →
// fan-out background extractors. Each step lives in its own file
// (add_fine_candidates.go / add_fine_actions.go / add_fine_async.go); this
// file only sequences them.

import (
	"context"
	"log/slog"
	"time"
)

// nativeFineAddForCube implements the fine-mode add pipeline (v2) for a single cube.
// Steps emit per-stage timing into memdb.add.stage_duration_ms{stage=...}.
//
// M11 F8: when MEMDB_ATOMIC_FACTS=true, delegates to runAtomicFineForCube,
// which uses mem0's verbatim ADDITIVE_EXTRACTION_PROMPT (atomic per-fact
// extraction). Default-off preserves the legacy single-paragraph path
// byte-identical.
func (h *Handler) nativeFineAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	if atomicFactsEnabled() {
		return h.runAtomicFineForCube(ctx, req, cubeID)
	}
	if len(req.Messages) == 0 {
		return nil, nil
	}
	now := nowTimestamp()
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
		CustomTags: req.CustomTags,
		Sources:    buildSourcesFromMessages(req.Messages),
		Key:        stringOrEmpty(req.Key),
	}
	items, err := h.applyAndPersistFineFacts(ctx, embedded, fc)
	recordStageDuration(ctx, "apply", t)
	if err != nil {
		return nil, err
	}

	// Stage 5: fire-and-forget background extractors (5 today, +F3 soon).
	t = time.Now()
	h.triggerBackgroundExtractors(extractorTriggerInput{
		CubeID: cubeID, UserID: *req.UserID, SessionID: sessionID,
		Conversation: conversation, Now: now,
		FactCount: len(facts), MessageCount: len(req.Messages),
	})
	recordStageDuration(ctx, "fanout", t)
	return items, nil
}
