package embedder

import (
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

const httpEmbedTimeout = 30 * time.Second

// HTTPEmbedder calls a remote OpenAI-compatible /v1/embeddings endpoint.
// Designed for the Rust embed-server sidecar on the internal Docker network.
type HTTPEmbedder struct {
	baseURL string
	model   string
	dim     int
	client  *http.Client
	logger  *slog.Logger
}

// NewHTTPEmbedder creates an HTTPEmbedder pointing at baseURL.
// baseURL should not include /v1/embeddings — it will be appended automatically.
func NewHTTPEmbedder(baseURL, model string, dim int, logger *slog.Logger) *HTTPEmbedder {
	return &HTTPEmbedder{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		dim:     dim,
		client:  &http.Client{Timeout: httpEmbedTimeout},
		logger:  logger,
	}
}

type httpEmbedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type httpEmbedResponse struct {
	Data []httpEmbedData `json:"data"`
}

type httpEmbedData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// Embed sends texts to the remote embedding server and returns vectors.
func (h *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(httpEmbedRequest{Input: texts, Model: h.model})
	if err != nil {
		return nil, fmt.Errorf("http embedder: marshal: %w", err)
	}

	url := h.baseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("http embedder: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http embedder: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("http embedder: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http embedder: status %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed httpEmbedResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("http embedder: unmarshal: %w", err)
	}

	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("http embedder: expected %d embeddings, got %d", len(texts), len(parsed.Data))
	}

	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("http embedder: invalid index %d", d.Index)
		}
		out[d.Index] = d.Embedding
	}

	h.logger.Debug("http embed complete",
		slog.Int("texts", len(texts)),
		slog.String("model", h.model),
	)
	return out, nil
}

// EmbedQuery embeds a single query string by delegating to Embed.
func (h *HTTPEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return EmbedQueryViaEmbed(ctx, h, text)
}

// Dimension returns the configured embedding dimension.
func (h *HTTPEmbedder) Dimension() int { return h.dim }

// Close is a no-op for the HTTP-based embedder.
func (h *HTTPEmbedder) Close() error { return nil }
