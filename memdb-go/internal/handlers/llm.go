package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

const (
	sseInitBufSize = 64 * 1024  // 64 KB initial SSE scanner buffer
	sseMaxBufSize  = 512 * 1024 // 512 KB max SSE scanner buffer
)

// NativeLLMComplete pass-through-routes OpenAI-compatible chat completions to
// CLIProxyAPI. It talks directly to CLIProxyAPI — not to the Python backend —
// making it a lightweight path without memory retrieval overhead (unlike
// /product/chat/complete which adds 60-80 s of memory search).
//
// Request: {messages: [{role, content}], model?, max_tokens?, temperature?}
// Response: OpenAI-compatible chat completions response (pass-through)
func (h *Handler) NativeLLMComplete(w http.ResponseWriter, r *http.Request) {
	if h.llmChat == nil {
		h.writeJSON(w, http.StatusServiceUnavailable,
			map[string]any{"code": 503, "message": "llm not configured", "data": nil})
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	body = injectDefaultModel(body, h.llmChat.Model())
	isStream := detectStreamMode(r, body)

	h.llmChat.Passthrough(r.Context(), body, isStream, w, h.logger)
}

// injectDefaultModel sets the model field to defaultModel if absent or empty.
func injectDefaultModel(body []byte, defaultModel string) []byte {
	var req map[string]any
	if json.Unmarshal(body, &req) != nil {
		return body
	}
	if _, hasModel := req["model"]; !hasModel || req["model"] == "" {
		req["model"] = defaultModel
		if patched, err := json.Marshal(req); err == nil {
			return patched
		}
	}
	return body
}

// detectStreamMode returns true when the request signals SSE streaming.
func detectStreamMode(r *http.Request, body []byte) bool {
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	var req map[string]any
	if json.Unmarshal(body, &req) == nil {
		if v, ok := req["stream"]; ok {
			if b, ok := v.(bool); ok && b {
				return true
			}
		}
	}
	return false
}
