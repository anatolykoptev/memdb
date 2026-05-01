package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// StreamChunk represents one chunk from a streaming response.
type StreamChunk struct {
	Delta        string
	FinishReason string
}

// StreamResponse reads chunks from a streaming chat completion.
type StreamResponse struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	usage   *Usage
	err     error
	done    bool
}

// streamEvent is the SSE JSON payload for a streaming chunk.
type streamEvent struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			Role    string `json:"role"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

// Next returns the next chunk. Returns false when streaming is done or on error.
// Check Err() after Next returns false.
func (s *StreamResponse) Next() (StreamChunk, bool) {
	if s.done || s.err != nil {
		return StreamChunk{}, false
	}
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			s.done = true
			return StreamChunk{}, false
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			s.err = err
			return StreamChunk{}, false
		}
		if event.Usage != nil {
			s.usage = event.Usage
		}
		if len(event.Choices) == 0 {
			continue
		}
		chunk := StreamChunk{
			Delta:        event.Choices[0].Delta.Content,
			FinishReason: event.Choices[0].FinishReason,
		}
		if chunk.Delta != "" || chunk.FinishReason != "" {
			return chunk, true
		}
	}
	if err := s.scanner.Err(); err != nil {
		s.err = err
	}
	s.done = true
	return StreamChunk{}, false
}

// Err returns any error encountered during streaming.
func (s *StreamResponse) Err() error { return s.err }

// Close closes the underlying response body.
func (s *StreamResponse) Close() error { return s.body.Close() }

// Usage returns token usage. Available after streaming completes.
func (s *StreamResponse) Usage() *Usage { return s.usage }

// Stream starts a streaming chat completion. The caller must call Close() when done.
func (c *Client) Stream(ctx context.Context, messages []Message, opts ...ChatOption) (*StreamResponse, error) {
	var cfg chatConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	req := c.newRequest(messages)
	req.Stream = true
	cfg.apply(req)

	if len(c.endpoints) > 0 {
		var lastErr error
		for _, ep := range c.endpoints {
			epReq := *req
			if ep.Model != "" {
				epReq.Model = ep.Model
			}
			sr, err := c.doStreamRequest(ctx, ep.URL, ep.Key, &epReq)
			if err == nil {
				return sr, nil
			}
			lastErr = err
			if !asRetryable(err) {
				return nil, err
			}
		}
		return nil, lastErr
	}

	keys := make([]string, 0, 1+len(c.fallbackKeys))
	keys = append(keys, c.apiKey)
	keys = append(keys, c.fallbackKeys...)

	var lastErr error
	for _, key := range keys {
		if key == "" {
			continue
		}
		sr, err := c.doStreamRequest(ctx, c.baseURL, key, req)
		if err == nil {
			return sr, nil
		}
		lastErr = err
		if !asRetryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) doStreamRequest(ctx context.Context, baseURL, apiKey string, req *ChatRequest) (*StreamResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, newAPIError(resp.StatusCode, string(respBody), isRetryableStatus(resp.StatusCode))
	}

	return &StreamResponse{
		body:    resp.Body,
		scanner: bufio.NewScanner(resp.Body),
	}, nil
}
