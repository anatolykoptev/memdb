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
	streamChunkBufSize       = 8 // buffered channel capacity for stream chunks
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
	chunks := make(chan StreamChunk, streamChunkBufSize)
	errc := make(chan error, 1)

	go c.runStream(ctx, messages, opts, chunks, errc)

	return chunks, errc
}

func (c *Client) runStream(ctx context.Context, messages []map[string]string, opts StreamOpts, chunks chan<- StreamChunk, errc chan<- error) {
	defer close(chunks)
	defer close(errc)

	resp, err := c.doStreamRequest(ctx, messages, opts)
	if err != nil {
		errc <- err
		return
	}
	defer resp.Body.Close()

	c.readSSEChunks(ctx, resp, chunks, errc)
}

// doStreamRequest builds and sends the streaming HTTP request.
func (c *Client) doStreamRequest(ctx context.Context, messages []map[string]string, opts StreamOpts) (*http.Response, error) {
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
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := streamHTTPClient.Do(req) //nolint:gosec // URL from trusted config
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("llm stream error %d", resp.StatusCode)
	}

	return resp, nil
}

// readSSEChunks reads SSE lines from the response and sends parsed chunks.
func (c *Client) readSSEChunks(ctx context.Context, resp *http.Response, chunks chan<- StreamChunk, errc chan<- error) {
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

		if content := c.parseSSEDelta(data); content != "" {
			chunks <- StreamChunk{Content: content}
		}
	}
	if err := scanner.Err(); err != nil {
		errc <- fmt.Errorf("read stream: %w", err)
		return
	}
	// Stream ended without [DONE] — still signal completion.
	chunks <- StreamChunk{Done: true}
}

// parseSSEDelta extracts the content delta from an SSE data JSON payload.
func (c *Client) parseSSEDelta(data string) string {
	var ev struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		c.logger.Warn("llm stream: bad chunk", slog.String("err", err.Error()))
		return ""
	}
	if len(ev.Choices) > 0 {
		return ev.Choices[0].Delta.Content
	}
	return ""
}
