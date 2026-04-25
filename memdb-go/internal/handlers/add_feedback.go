// Package handlers — Go-native feedback processing (POST /product/feedback).
// Ports Python mem_feedback/feedback.py: detect keyword-replace OR judge feedback
// → recall related memories per judgement → decide ADD/UPDATE → safety judge → apply.
package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	feedbackProcessingTimeout = 120 * time.Second
	feedbackRecallTopK        = 8
	feedbackMaxChatHistory    = 4
)

// NativeFeedback processes POST /product/feedback fully in Go, replacing the
// Python proxy path. Sync response: {"record": {"add":[...], "update":[...]}}.
func (h *Handler) NativeFeedback(w http.ResponseWriter, r *http.Request) {
	if !h.feedbackCanNative() {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "feedback: backend not ready",
			"data":    nil,
		})
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	var req feedbackRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}
	if !h.checkErrors(w, validateFeedbackRequest(req)) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), feedbackProcessingTimeout)
	defer cancel()

	resp, err := h.processFeedback(ctx, req)
	if err != nil {
		h.logger.Error("feedback: processing failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    500,
			"message": "feedback processing failed",
			"data":    nil,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// feedbackCanNative returns true when all dependencies are initialised.
func (h *Handler) feedbackCanNative() bool {
	return h.postgres != nil && h.embedder != nil && h.llmChat != nil
}

// validateFeedbackRequest — mirrors ValidatedFeedback's checks.
func validateFeedbackRequest(req feedbackRequest) []string {
	var errs []string
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.FeedbackContent == nil || *req.FeedbackContent == "" {
		errs = append(errs, "feedback_content is required")
	}
	if req.History == nil {
		errs = append(errs, "history is required")
	}
	return errs
}

// processFeedback is the stub filled in by Tasks 2-4.
// It persists a feedback_events row (fire-and-forget) before returning.
func (h *Handler) processFeedback(ctx context.Context, req feedbackRequest) (*feedbackResponse, error) {
	_ = llm.KeywordReplaceResult{} // keep import used during skeleton phase

	// Persist a feedback_events row for the M11 reward loop.
	// This is intentionally fire-and-forget: errors are logged and metered,
	// but never propagated to the caller.
	h.persistFeedbackEvent(req)

	return &feedbackResponse{}, nil
}

// persistFeedbackEvent writes a row to memos_graph.feedback_events in a detached
// goroutine. Errors are logged and counted; the calling request is never blocked.
// Label is always "neutral" at the scaffold stage — M11 will derive it from LLM judge.
func (h *Handler) persistFeedbackEvent(req feedbackRequest) {
	// Capture fields needed inside the goroutine (avoid req escape).
	userID := *req.UserID
	query := *req.FeedbackContent
	// prediction is not yet computed (processFeedback is a stub).
	// We store an empty prediction string until Tasks 2-4 fill the pipeline.
	prediction := ""
	label := "neutral"

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := h.postgres.InsertFeedbackEvent(ctx, db.InsertFeedbackEventParams{
			UserID:     userID,
			Query:      query,
			Prediction: prediction,
			Label:      label,
		})
		mx := feedbackEventsMx()
		if err != nil {
			h.logger.Warn("feedback: persist event failed", slog.Any("error", err))
			// Count the failure via the metric using a synthetic label so dashboards
			// can detect write failures without a separate error counter.
			mx.EventsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("label", "error")))
			return
		}
		mx.EventsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("label", label)))
	}()
}
