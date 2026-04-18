package handlers

// feedback.go — feedback pipeline entry point and routing.
// Handles is_feedback=true requests natively in Go (previously proxied to Python).

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// handleFeedback processes a feedback request for a single cube.
// Pipeline:
//  1. Extract feedbackContent (last user message) and chatHistory (messages before it)
//  2. Empty check → return empty
//  3. Detect keyword replace → processKeywordReplace
//  4. If no chat history → processPureAdd (run normal extraction)
//  5. Judge feedback → filter valid items
//  6. If no valid items + irrelevant attitude → processPureAdd
//  7. For each valid judgement → build feedback items
//  8. Process semantic feedback → find related memories → decide ops → execute
func (h *Handler) handleFeedback(ctx context.Context, cubeID string, req *fullAddRequest) ([]addResponseItem, error) {
	start := time.Now()
	mx := feedbackMx()
	mx.Requests.Add(ctx, 1, metric.WithAttributes(
		attribute.String("entry", "native_add_pipeline"),
	))

	outcome := "unknown"
	defer func() {
		mx.Duration.Record(ctx, float64(time.Since(start).Milliseconds()),
			metric.WithAttributes(attribute.String("outcome", outcome)))
		mx.Operations.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}()

	userID := *req.UserID
	if len(req.Messages) == 0 {
		outcome = "empty"
		return nil, nil
	}

	// Extract feedback content (last user message) and chat history (everything before)
	feedbackContent, chatHistory := splitFeedback(req.Messages)
	if feedbackContent == "" {
		outcome = "empty"
		return nil, nil
	}

	now := nowTimestamp()

	// Step 1: detect keyword replacement
	if h.llmChat != nil {
		krResult, err := llm.DetectKeywordReplace(ctx, h.llmChat, feedbackContent)
		if err != nil {
			h.logger.Debug("feedback: keyword replace detection failed", slog.Any("error", err))
		} else if krResult.IsReplace() {
			h.logger.Debug("feedback: keyword replace detected",
				slog.String("original", krResult.Original),
				slog.String("target", krResult.Target))
			outcome = "keyword_replace"
			return h.processKeywordReplace(ctx, cubeID, krResult)
		}
	}

	// Step 2: no chat history → treat as pure add
	if chatHistory == "" {
		h.logger.Debug("feedback: no chat history, running pure add")
		outcome = "pure_add_no_history"
		return h.processPureAdd(ctx, cubeID, userID, feedbackContent)
	}

	// Step 3: judge feedback quality and extract corrected info
	if h.llmChat == nil {
		outcome = "pure_add_no_history"
		return h.processPureAdd(ctx, cubeID, userID, feedbackContent)
	}

	judgements, err := llm.JudgeFeedback(ctx, h.llmChat, chatHistory, feedbackContent, now)
	if err != nil {
		h.logger.Debug("feedback: judgement failed, falling back to pure add", slog.Any("error", err))
		outcome = "pure_add_no_history"
		return h.processPureAdd(ctx, cubeID, userID, feedbackContent)
	}

	// Filter valid judgements
	var validItems []feedbackItem
	allIrrelevant := true
	for _, j := range judgements {
		if j.UserAttitude != "irrelevant" {
			allIrrelevant = false
		}
		if j.Validity != "true" || j.CorrectedInfo == "" {
			continue
		}
		validItems = append(validItems, feedbackItem{
			correctedInfo: j.CorrectedInfo,
			key:           j.Key,
			tags:          j.Tags,
		})
	}

	// No valid items + all irrelevant → pure add
	if len(validItems) == 0 {
		if allIrrelevant {
			h.logger.Debug("feedback: all irrelevant, running pure add")
			outcome = "pure_add_irrelevant"
			return h.processPureAdd(ctx, cubeID, userID, feedbackContent)
		}
		h.logger.Debug("feedback: no valid items extracted")
		outcome = "no_valid_items"
		return nil, nil
	}

	h.logger.Debug("feedback: valid items extracted", slog.Int("count", len(validItems)))

	// Step 4: process semantic feedback
	outcome = "semantic"
	return h.processSemanticFeedback(ctx, cubeID, userID, validItems, chatHistory, now)
}

// feedbackItem holds extracted feedback info ready for semantic processing.
type feedbackItem struct {
	correctedInfo string
	key           string
	tags          []string
}

// splitFeedback extracts the last user message as feedback and everything before as chat history.
func splitFeedback(messages []chatMessage) (feedback, chatHistory string) {
	if len(messages) == 0 {
		return "", ""
	}

	// Find the last user message
	lastIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastIdx = i
			break
		}
	}
	if lastIdx < 0 {
		return "", ""
	}

	feedback = messages[lastIdx].Content

	// Build chat history from messages before the last user message
	if lastIdx > 0 {
		var sb strings.Builder
		for _, msg := range messages[:lastIdx] {
			fmt.Fprintf(&sb, "%s: %s\n", msg.Role, msg.Content)
		}
		chatHistory = strings.TrimSpace(sb.String())
	}

	return feedback, chatHistory
}
