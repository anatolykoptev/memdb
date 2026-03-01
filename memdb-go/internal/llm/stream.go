package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

const (
	streamScannerInitBuf = 64 * 1024  // 64 KB
	streamScannerMaxBuf  = 512 * 1024 // 512 KB

	streamDefaultMaxTokens   = 8192
	streamDefaultTemperature = 0.1
)

// streamHTTPClient has no timeout — SSE streams are long-lived.
var streamHTTPClient = &http.Client{} //nolint:gochecknoglobals // shared across all streaming calls

// StreamOpts configures a streaming chat completion.
type StreamOpts struct {
	MaxTokens   int
	Temperature float64
	Model       string // override primary model (empty = use c.model)
}

// StreamChunk is a single piece of a streaming response.
type StreamChunk struct {
	Content string // text delta
	Done    bool   // true on final chunk
}

// ChatStream sends a streaming chat completion and returns channels for
// content chunks and a single error (or nil on success).
func (c *Client) ChatStream(ctx context.Context, messages []map[string]string, opts StreamOpts) (<-chan StreamChunk, <-chan error) {
	chunks := make(chan StreamChunk, 8)
	errc := make(chan error, 1)

	go c.runStream(ctx, messages, opts, chunks, errc)

	return chunks, errc
}

func (c *Client) runStream(ctx context.Context, messages []map[string]string, opts StreamOpts, chunks chan<- StreamChunk, errc chan<- error) {
	defer close(chunks)
	defer close(errc)

	model := opts.Model
	if model == "" {
		model = c.model
	}
	temp := opts.Temperature
	if temp == 0 {
		temp = streamDefaultTemperature
	}
	maxTok := opts.MaxTokens
	if maxTok == 0 {
		maxTok = streamDefaultMaxTokens
	}

	body, err := json.Marshal(map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": temp,
		"max_tokens":  maxTok,
		"stream":      true,
	})
	if err != nil {
		errc <- fmt.Errorf("marshal request: %w", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		errc <- fmt.Errorf("create request: %w", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := streamHTTPClient.Do(req)
	if err != nil {
		errc <- fmt.Errorf("request: %w", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errc <- fmt.Errorf("llm stream error %d", resp.StatusCode)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, streamScannerInitBuf), streamScannerMaxBuf)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			errc <- ctx.Err()
			return
		default:
		}

		line := scanner.Text()
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			chunks <- StreamChunk{Done: true}
			return
		}

		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			c.logger.Warn("llm stream: bad chunk", slog.String("err", err.Error()))
			continue
		}
		if len(ev.Choices) > 0 && ev.Choices[0].Delta.Content != "" {
			chunks <- StreamChunk{Content: ev.Choices[0].Delta.Content}
		}
	}
	if err := scanner.Err(); err != nil {
		errc <- fmt.Errorf("read stream: %w", err)
		return
	}
	// Stream ended without [DONE] — still signal completion.
	chunks <- StreamChunk{Done: true}
}
