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
	maxRetries   = 3
	initialDelay = 2 * time.Second
	maxDelay     = 30 * time.Second
)

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
		for attempt := range maxRetries {
			content, apiErr := c.chatOnce(ctx, model, messages, maxTokens)
			if apiErr == nil {
				return content, nil
			}
			lastErr = apiErr

			// Auth errors: fail immediately, no retry.
			if apiErr.IsAuth() {
				return "", apiErr
			}

			// Quota error with more models available: break to try next model.
			if apiErr.isQuotaError() && i < len(models)-1 {
				c.logger.Warn("llm quota error, switching model",
					slog.String("model", model),
					slog.Int("status", apiErr.StatusCode),
				)
				break
			}

			// Transient error with more attempts: backoff and retry.
			if apiErr.IsTransient() && attempt < maxRetries-1 {
				delay := backoff(attempt)
				c.logger.Warn("llm transient error, retrying",
					slog.String("model", model),
					slog.Int("status", apiErr.StatusCode),
					slog.Int("attempt", attempt+1),
					slog.Duration("delay", delay),
				)
				if sleepCtx(ctx, delay) != nil {
					return "", lastErr
				}
				continue
			}

			// Non-transient or last attempt: stop retrying this model.
			break
		}

		// If it was a quota error and there are more models, try next.
		if lastErr != nil {
			var apiErr *APIError
			if isAPIError(lastErr, &apiErr) && apiErr.isQuotaError() && i < len(models)-1 {
				c.logger.Info("llm model fallback",
					slog.String("from", model),
					slog.String("to", models[i+1]),
				)
				continue
			}
		}
		break
	}
	return "", lastErr
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
	return fmt.Sprintf("llm error: %s", e.Message)
}

// IsAuth returns true for 401/403 authentication errors.
func (e *APIError) IsAuth() bool { return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden }

// IsRateLimit returns true for 429 rate limit errors.
func (e *APIError) IsRateLimit() bool { return e.StatusCode == http.StatusTooManyRequests }

// IsTransient returns true for errors worth retrying (429, 5xx).
func (e *APIError) IsTransient() bool { return e.IsRateLimit() || e.StatusCode >= http.StatusInternalServerError }

// isQuotaError returns true for 429 or body containing "quota"/"rate limit".
func (e *APIError) isQuotaError() bool {
	if e.IsRateLimit() {
		return true
	}
	lower := strings.ToLower(e.Message)
	return strings.Contains(lower, "quota") || strings.Contains(lower, "rate limit")
}

// isAPIError unwraps err into an *APIError (type assertion, not errors.As —
// APIError is always returned directly by chatOnce).
func isAPIError(err error, target **APIError) bool {
	ae, ok := err.(*APIError)
	if ok {
		*target = ae
	}
	return ok
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
	jitter := time.Duration(rand.Int64N(int64(delay / 4)))
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
