// Package llm provides a shared LLM client with retry and model fallback.
//
// Retry: up to 3 attempts with exponential backoff (2s base, 2x multiplier,
// 30s cap, jitter). Auth errors (401/403) fail immediately.
//
// Model fallback: on quota errors (429 or body containing "quota"/"rate limit"),
// the client tries each fallback model in order before giving up.
//
// Based on patterns from dozor (retry) and go-hully (model fallback).
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

const (
	maxRetries    = 3
	initialDelay  = 2 * time.Second
	maxDelay      = 30 * time.Second
	jitterDivisor = 4 // jitter = rand(0, delay/jitterDivisor) for retry backoff

	passthroughInitBuf = 64 * 1024  // 64 KB initial SSE scanner buffer
	passthroughMaxBuf  = 512 * 1024 // 512 KB max SSE scanner buffer
)

// passthroughSSEClient has no Timeout — SSE streams terminate via ctx cancellation,
// not wall-clock. Shared across Passthrough calls.
var passthroughSSEClient = &http.Client{}

// Passthrough forwards an already-serialized OpenAI-compatible chat completion body
// to the configured upstream, copying status and headers back to w. When isStream is
// true the response body is streamed line-by-line (SSE) with explicit Flusher pumps.
//
// On transport error the response is a JSON 502. All errors are logged via logger.
// Intended for the /product/llm/complete HTTP adapter — no retries, no fallback
// models, no memory retrieval.
func (c *Client) Passthrough(ctx context.Context, body []byte, isStream bool, w http.ResponseWriter, logger *slog.Logger) {
	target := c.baseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		logger.Error("llm passthrough: create request failed", slog.Any("error", err))
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	client := c.httpClient
	if isStream {
		client = passthroughSSEClient
	}

	resp, err := client.Do(req)
	if err != nil {
		logger.Error("llm passthrough: request failed",
			slog.String("target", target), slog.Any("error", err))
		writeJSONError(w, http.StatusBadGateway, "llm service unavailable")
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(resp.StatusCode)
		streamSSELines(ctx, w, resp.Body, logger)
		return
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// streamSSELines proxies an SSE body to the client using line-oriented scanning.
// Guarantees SSE field boundaries are never split across reads.
func streamSSELines(ctx context.Context, w http.ResponseWriter, body io.Reader, logger *slog.Logger) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, passthroughInitBuf), passthroughMaxBuf)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				logger.Debug("llm passthrough sse: scanner error", slog.Any("error", err))
			}
			return
		}
		fmt.Fprintf(w, "%s\n", scanner.Text())
		flusher.Flush()
	}
}

// writeJSONError is a tiny helper so Passthrough does not depend on handlers.writeJSON.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `{"code":%d,"message":%q,"data":null}`, code, msg)
}

// Client is an OpenAI-compatible chat completion client with retry and
// model fallback on quota errors.
type Client struct {
	httpClient     *http.Client
	baseURL        string
	apiKey         string
	model          string
	fallbackModels []string
	logger         *slog.Logger
}

// NewClient creates a Client. baseURL should include scheme+host (no trailing
// slash or /v1 suffix — the client appends /v1/chat/completions).
func NewClient(baseURL, apiKey, model string, fallbackModels []string, logger *slog.Logger) *Client {
	return &Client{
		httpClient:     &http.Client{Timeout: 90 * time.Second},
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		model:          model,
		fallbackModels: fallbackModels,
		logger:         logger,
	}
}

// Model returns the primary model name.
func (c *Client) Model() string { return c.model }

// Chat sends a chat completion with retry + model fallback.
//
// Algorithm:
//
//	models = [primary] + fallbackModels
//	for each model:
//	    for attempt 0..2:
//	        if success: return
//	        if auth error: fail immediately
//	        if quota error && more models: break to next model
//	        if transient && more attempts: backoff + retry
//	    if quota error && more models: continue
//	    return error
func (c *Client) Chat(ctx context.Context, messages []map[string]string, maxTokens int) (string, error) {
	models := make([]string, 0, 1+len(c.fallbackModels))
	models = append(models, c.model)
	models = append(models, c.fallbackModels...)

	var lastErr error
	for i, model := range models {
		content, err, switchModel := c.chatModelLoop(ctx, model, models, i, messages, maxTokens)
		if err != nil {
			return "", err
		}
		if content != "" {
			return content, nil
		}
		lastErr = switchModel
		if lastErr == nil {
			break
		}
	}
	return "", lastErr
}

// retryDecision describes the outcome of a single attempt.
type retryDecision int

const (
	retryStop      retryDecision = iota // no more retries for this model
	retryContinue                       // retry with same model (transient)
	retryNextModel                      // switch to next model (quota)
)

// chatModelLoop runs the retry loop for one model.
// Returns: (content, fatalErr, lastErr) — content set on success, fatalErr on auth failure,
// lastErr holds the last error for the caller to surface if no fallback exists.
func (c *Client) chatModelLoop(ctx context.Context, model string, models []string, modelIdx int, messages []map[string]string, maxTokens int) (string, error, error) {
	hasNext := modelIdx < len(models)-1
	var lastErr error

	for attempt := range maxRetries {
		content, apiErr := c.chatOnce(ctx, model, messages, maxTokens)
		if apiErr == nil {
			return content, nil, nil
		}
		lastErr = apiErr

		decision := c.classifyAttemptError(ctx, apiErr, model, attempt, hasNext)
		switch decision {
		case retryContinue:
			continue
		case retryNextModel:
			if hasNext {
				c.logger.Info("llm model fallback",
					slog.String("from", model), slog.String("to", models[modelIdx+1]))
				return "", nil, lastErr
			}
		}
		// retryStop or no fallback
		break
	}
	return "", nil, lastErr
}

// classifyAttemptError determines what to do after a failed attempt and applies side effects
// (logging, backoff sleep). Returns the retry decision.
func (c *Client) classifyAttemptError(ctx context.Context, apiErr *APIError, model string, attempt int, hasNext bool) retryDecision {
	if apiErr.IsAuth() {
		return retryStop
	}
	if apiErr.isQuotaError() && hasNext {
		c.logger.Warn("llm quota error, switching model",
			slog.String("model", model), slog.Int("status", apiErr.StatusCode))
		return retryNextModel
	}
	if apiErr.IsTransient() && attempt < maxRetries-1 {
		delay := backoff(attempt)
		c.logger.Warn("llm transient error, retrying",
			slog.String("model", model), slog.Int("status", apiErr.StatusCode),
			slog.Int("attempt", attempt+1), slog.Duration("delay", delay))
		_ = sleepCtx(ctx, delay)
		return retryContinue
	}
	return retryStop
}

// chatOnce performs a single HTTP round-trip to the chat completions endpoint.
func (c *Client) chatOnce(ctx context.Context, model string, messages []map[string]string, maxTokens int) (string, *APIError) {
	body, err := json.Marshal(map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": 0.1,
		"max_tokens":  maxTokens,
	})
	if err != nil {
		return "", &APIError{Message: fmt.Sprintf("marshal request: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", &APIError{Message: fmt.Sprintf("create request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Network error — treat as transient 500.
		return "", &APIError{StatusCode: http.StatusInternalServerError, Message: fmt.Sprintf("request: %v", err)}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", &APIError{StatusCode: http.StatusInternalServerError, Message: fmt.Sprintf("read body: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		return "", parseAPIError(resp.StatusCode, data)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", &APIError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("decode response: %v", err)}
	}
	if result.Error != nil {
		return "", &APIError{StatusCode: resp.StatusCode, Message: result.Error.Message}
	}
	if len(result.Choices) == 0 {
		return "", &APIError{StatusCode: resp.StatusCode, Message: "no choices in response"}
	}
	return result.Choices[0].Message.Content, nil
}

// --- Error classification ---

// APIError is a structured error from the LLM API.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("llm api error %d: %s", e.StatusCode, e.Message)
	}
	return "llm error: " + e.Message
}

// IsAuth returns true for 401/403 authentication errors.
func (e *APIError) IsAuth() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}

// IsRateLimit returns true for 429 rate limit errors.
func (e *APIError) IsRateLimit() bool { return e.StatusCode == http.StatusTooManyRequests }

// IsTransient returns true for errors worth retrying (429, 5xx).
func (e *APIError) IsTransient() bool {
	return e.IsRateLimit() || e.StatusCode >= http.StatusInternalServerError
}

// isQuotaError returns true for 429 or body containing "quota"/"rate limit".
func (e *APIError) isQuotaError() bool {
	if e.IsRateLimit() {
		return true
	}
	lower := strings.ToLower(e.Message)
	return strings.Contains(lower, "quota") || strings.Contains(lower, "rate limit")
}

// parseAPIError parses a non-200 response body into an APIError.
func parseAPIError(statusCode int, body []byte) *APIError {
	ae := &APIError{StatusCode: statusCode}

	// Try OpenAI-compat format: {"error": {"message": "..."}}
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		ae.Message = parsed.Error.Message
		return ae
	}

	// Fallback: first line of body, truncated.
	s := strings.TrimSpace(string(body))
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		s = s[:idx]
	}
	const maxMsg = 300
	if len(s) > maxMsg {
		s = s[:maxMsg] + "..."
	}
	ae.Message = s
	return ae
}

// --- Backoff helpers ---

func backoff(attempt int) time.Duration {
	delay := initialDelay
	for range attempt {
		delay *= 2
	}
	jitter := time.Duration(rand.Int64N(int64(delay / jitterDivisor))) //nolint:gosec // jitter for retry backoff does not require cryptographic randomness
	delay += jitter
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
