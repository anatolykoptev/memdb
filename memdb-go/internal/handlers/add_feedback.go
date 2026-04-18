// Package handlers — Go-native feedback processing (POST /product/feedback).
// Ports Python mem_feedback/feedback.py: detect keyword-replace OR judge feedback
// → recall related memories per judgement → decide ADD/UPDATE → safety judge → apply.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

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
		http.Error(w, fmt.Sprintf("feedback failed: %v", err), http.StatusInternalServerError)
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
func (h *Handler) processFeedback(ctx context.Context, req feedbackRequest) (*feedbackResponse, error) {
	_ = llm.KeywordReplaceResult{} // keep import used during skeleton phase
	return &feedbackResponse{}, nil
}
