package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

func (c *Client) doWithRetry(ctx context.Context, baseURL, apiKey string, req *ChatRequest) (*ChatResponse, error) {
	delay := retryDelay
	var lastErr error

	for attempt := range c.maxRetries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			delay = min(delay*2, maxRetryDelay)
		}

		result, err := c.doRequest(ctx, baseURL, apiKey, req)
		if err == nil {
			return result, nil
		}
		lastErr = err

		// Only retry on retryable errors.
		if !asRetryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) doRequest(ctx context.Context, baseURL, apiKey string, req *ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(httpReq) //nolint:gosec // G704: URL comes from caller config, not user input
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, newAPIError(resp.StatusCode, string(respBody), isRetryableStatus(resp.StatusCode))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, errors.New("llm: empty choices in response")
	}

	return &ChatResponse{
		Content:      strings.TrimSpace(chatResp.Choices[0].Message.Content),
		ToolCalls:    chatResp.Choices[0].Message.ToolCalls,
		FinishReason: chatResp.Choices[0].FinishReason,
		Usage:        chatResp.Usage,
	}, nil
}

func (c *Client) executeInner(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if len(c.endpoints) > 0 {
		var lastErr error
		for _, ep := range c.endpoints {
			epReq := *req
			if ep.Model != "" {
				epReq.Model = ep.Model
			}
			result, err := c.doWithRetry(ctx, ep.URL, ep.Key, &epReq)
			if err == nil {
				return result, nil
			}
			lastErr = err
			if !asRetryable(err) {
				return nil, err
			}
		}
		return nil, lastErr
	}
	result, err := c.doWithRetry(ctx, c.baseURL, c.apiKey, req)
	if err == nil {
		return result, nil
	}
	for _, key := range c.fallbackKeys {
		if key == "" {
			continue
		}
		result, err = c.doWithRetry(ctx, c.baseURL, key, req)
		if err == nil {
			return result, nil
		}
	}
	return nil, err
}
