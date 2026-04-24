package handlers

// chat.go — native chat complete and streaming handlers.
// Falls back to Python proxy when services are unavailable or on error.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/rpc"
)

const (
	chatMaxHistory = 20   // last N history messages kept for LLM context
	chatMaxTokens  = 8192 // max tokens for chat completion

	// answer_style enum values (see nativeChatRequest.AnswerStyle).
	answerStyleFactual        = "factual"
	answerStyleConversational = "conversational"
)

// promptTemplateLabel maps the (basePrompt, answerStyle) pair to the metric label
// emitted by chat handlers. "custom" wins when basePrompt is non-empty (backward-compat).
func promptTemplateLabel(basePrompt, answerStyle string) string {
	if basePrompt != "" {
		return "custom"
	}
	if answerStyle == answerStyleFactual {
		return answerStyleFactual
	}
	return answerStyleConversational
}

// recordChatPromptUsed bumps memdb.chat.prompt_template_used_total{template=...}.
// Called once per chat request right after buildSystemPrompt returns.
func recordChatPromptUsed(ctx context.Context, basePrompt, answerStyle string) {
	chatPromptMx().TemplateUsed.Add(ctx, 1,
		metric.WithAttributes(attribute.String("template", promptTemplateLabel(basePrompt, answerStyle))),
	)
}

// chatCanNative returns true if all services needed for native chat are available.
func (h *Handler) chatCanNative() bool {
	return h.searchService != nil && h.searchService.CanSearch() && h.llmChat != nil
}

// resolveAnswerStyle returns the effective answer_style for a request.
// Request-level value always wins; if absent or empty and a server-wide default
// is configured (MEMDB_DEFAULT_ANSWER_STYLE), that default is used instead.
func (h *Handler) resolveAnswerStyle(req *nativeChatRequest) string {
	if req.AnswerStyle != nil && *req.AnswerStyle != "" {
		return *req.AnswerStyle
	}
	if h.cfg != nil && h.cfg.DefaultAnswerStyle != "" {
		return h.cfg.DefaultAnswerStyle
	}
	return stringOrEmpty(req.AnswerStyle)
}

// NativeChatComplete handles POST /product/chat/complete.
func (h *Handler) NativeChatComplete(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	var req nativeChatRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}
	if !h.checkErrors(w, validateChatRequest(&req)) {
		return
	}
	normalized := normalizeChatComplete(body)

	if !h.chatCanNative() {
		h.proxyWithBody(w, r, normalized)
		return
	}

	ctx := r.Context()
	memories, prefString, err := h.chatSearchMemories(ctx, &req)
	if err != nil {
		h.logger.Warn("chat search failed, proxying", slog.Any("error", err))
		h.proxyWithBody(w, r, normalized)
		return
	}

	basePrompt := stringOrEmpty(req.SystemPrompt)
	answerStyle := h.resolveAnswerStyle(&req)
	prompt := buildSystemPrompt(*req.Query, memories, prefString, basePrompt, answerStyle)
	recordChatPromptUsed(ctx, basePrompt, answerStyle)
	messages := chatBuildMessages(prompt, *req.Query, req.History)

	answer, err := h.llmChat.Chat(ctx, messages, chatMaxTokens)
	if err != nil {
		h.logger.Error("chat LLM error", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 500, "message": "LLM error: " + err.Error(),
		})
		return
	}

	response, reasoning := parseThinkTags(answer)

	if derefBoolOr(req.AddMessageOnAnswer, false) {
		h.chatPostAdd(&req, *req.Query, response)
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "Chat completed successfully",
		"data":    map[string]any{"response": response, "reasoning": reasoning},
	})
}

// NativeChatStream handles POST /product/chat and POST /product/chat/stream.
func (h *Handler) NativeChatStream(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	var req nativeChatRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}
	if !h.checkErrors(w, validateChatRequest(&req)) {
		return
	}
	normalized := normalizeChatComplete(body)

	if !h.chatCanNative() {
		h.proxyWithBody(w, r, normalized)
		return
	}

	ctx := r.Context()
	memories, prefString, err := h.chatSearchMemories(ctx, &req)
	if err != nil {
		h.logger.Warn("chat stream search failed, proxying", slog.Any("error", err))
		h.proxyWithBody(w, r, normalized)
		return
	}

	basePrompt := stringOrEmpty(req.SystemPrompt)
	answerStyle := h.resolveAnswerStyle(&req)
	prompt := buildSystemPrompt(*req.Query, memories, prefString, basePrompt, answerStyle)
	recordChatPromptUsed(ctx, basePrompt, answerStyle)
	messages := chatBuildMessages(prompt, *req.Query, req.History)

	rpc.SSEHeaders(w)
	sse := rpc.NewSSEWriter(w, h.logger)
	if sse == nil {
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": 500, "message": "streaming not supported",
		})
		return
	}

	chunks, errc := h.llmChat.ChatStream(ctx, messages, llm.StreamOpts{})
	h.streamChatResponse(sse, chunks, errc, &req)
}

// streamChatResponse reads chunks, classifies think tags, emits SSE events.
func (h *Handler) streamChatResponse(sse *rpc.SSEWriter, chunks <-chan llm.StreamChunk, errc <-chan error, req *nativeChatRequest) {
	parser := &thinkParser{}
	var fullResp strings.Builder

	for chunk := range chunks {
		if chunk.Done {
			break
		}
		for _, seg := range parser.Feed(chunk.Content) {
			h.emitSegment(sse, seg, &fullResp)
		}
	}
	// Flush any buffered partial-tag text.
	for _, seg := range parser.Flush() {
		h.emitSegment(sse, seg, &fullResp)
	}

	// Check for stream error.
	if err, ok := <-errc; ok && err != nil {
		data, _ := json.Marshal(map[string]string{"type": "error", "content": err.Error()})
		_ = sse.WriteData(string(data))
	}
	_ = sse.WriteDone()

	if derefBoolOr(req.AddMessageOnAnswer, false) {
		h.chatPostAdd(req, *req.Query, fullResp.String())
	}
}

// emitSegment writes a single classified segment as an SSE event.
func (h *Handler) emitSegment(sse *rpc.SSEWriter, seg chatSegment, fullResp *strings.Builder) {
	if seg.Text == "" {
		return
	}
	typ := "text"
	if seg.Reasoning {
		typ = "reasoning"
	} else {
		fullResp.WriteString(seg.Text)
	}
	data, _ := json.Marshal(map[string]string{"type": typ, "data": seg.Text})
	if err := sse.WriteData(string(data)); err != nil {
		h.logger.Debug("sse write failed", slog.Any("error", err))
	}
}

// validateChatRequest validates a nativeChatRequest.
func validateChatRequest(req *nativeChatRequest) []string {
	var errs []string
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.Query == nil || strings.TrimSpace(*req.Query) == "" {
		errs = append(errs, "query is required and must be non-empty")
	}
	if req.TopK != nil && *req.TopK < 1 {
		errs = append(errs, "top_k must be >= 1")
	}
	if req.AnswerStyle != nil {
		switch *req.AnswerStyle {
		case "", answerStyleFactual, answerStyleConversational:
			// allowed
		default:
			errs = append(errs, fmt.Sprintf(
				"unknown answer_style '%s', valid: factual, conversational",
				*req.AnswerStyle,
			))
		}
	}
	return errs
}
