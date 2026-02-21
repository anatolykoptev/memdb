package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

var llmProxyURL = "http://cliproxyapi:8317"
var llmProxyAPIKey string
var llmDefaultModel = "gemini-2.5-flash"

// SetLLMProxy configures the upstream LLM proxy URL, API key, and default model.
func SetLLMProxy(url, apiKey, defaultModel string) {
	if url != "" {
		llmProxyURL = url
	}
	llmProxyAPIKey = apiKey
	if defaultModel != "" {
		llmDefaultModel = defaultModel
	}
}

// llmClient is a shared HTTP client for non-streaming LLM proxy requests.
var llmClient = &http.Client{Timeout: 120 * time.Second}

// llmSSEClient has no Timeout — SSE streams are terminated by ctx cancellation.
var llmSSEClient = &http.Client{}

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

	// Inject default model if not specified by client
	var req map[string]any
	if json.Unmarshal(body, &req) == nil {
		if _, hasModel := req["model"]; !hasModel || req["model"] == "" {
			req["model"] = llmDefaultModel
			body, _ = json.Marshal(req)
		}
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

	// Detect streaming mode: client requested stream:true OR Accept: text/event-stream.
	isStream := false
	var reqMap map[string]any
	if json.Unmarshal(body, &reqMap) == nil {
		if v, ok := reqMap["stream"]; ok {
			if b, ok := v.(bool); ok && b {
				isStream = true
			}
		}
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		isStream = true
	}

	client := llmClient
	if isStream {
		client = llmSSEClient
	}

	resp, err := client.Do(proxyReq)
	if err != nil {
		h.logger.Error("llm proxy: request failed",
			slog.String("target", targetURL),
			slog.Any("error", err))
		h.writeJSON(w, http.StatusBadGateway,
			map[string]any{"code": 502, "message": "llm service unavailable", "data": nil})
		return
	}
	defer resp.Body.Close()

	// Pass through response headers.
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}

	// SSE streaming response — use scanner-based proxy.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(resp.StatusCode)
		h.streamSSELines(r.Context(), w, resp.Body)
		return
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// streamSSELines proxies an SSE body to the client using line-oriented scanning.
// Guarantees SSE field boundaries are never split across reads.
func (h *Handler) streamSSELines(ctx context.Context, w http.ResponseWriter, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 512*1024)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				h.logger.Debug("llm sse: scanner error", slog.Any("error", err))
			}
			return
		}

		fmt.Fprintf(w, "%s\n", scanner.Text())
		flusher.Flush()
	}
}
