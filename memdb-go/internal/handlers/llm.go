package handlers

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"time"
)

var llmProxyURL = "http://cliproxyapi:8317"
var llmProxyAPIKey string

// SetLLMProxy configures the upstream LLM proxy URL and API key (CLIProxyAPI).
func SetLLMProxy(url, apiKey string) {
	if url != "" {
		llmProxyURL = url
	}
	llmProxyAPIKey = apiKey
}

// llmClient is a shared HTTP client for LLM proxy requests.
var llmClient = &http.Client{Timeout: 120 * time.Second}

// ProxyLLMComplete forwards OpenAI-compatible chat completions to CLIProxyAPI.
// This is a lightweight LLM proxy without memory retrieval (unlike /product/chat/complete
// which adds 60-80s of memory search overhead).
//
// Request: {messages: [{role, content}], model?, max_tokens?, temperature?}
// Response: OpenAI-compatible chat completions response (pass-through)
func (h *Handler) ProxyLLMComplete(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	targetURL := llmProxyURL + "/v1/chat/completions"
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL,
		bytes.NewReader(body))
	if err != nil {
		h.logger.Error("llm proxy: create request failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError,
			map[string]any{"code": 500, "message": "internal error", "data": nil})
		return
	}

	proxyReq.Header.Set("Content-Type", "application/json")
	if llmProxyAPIKey != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+llmProxyAPIKey)
	}

	resp, err := llmClient.Do(proxyReq)
	if err != nil {
		h.logger.Error("llm proxy: request failed",
			slog.String("target", targetURL),
			slog.Any("error", err))
		h.writeJSON(w, http.StatusBadGateway,
			map[string]any{"code": 502, "message": "llm service unavailable", "data": nil})
		return
	}
	defer resp.Body.Close()

	// Pass through response headers and body
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
