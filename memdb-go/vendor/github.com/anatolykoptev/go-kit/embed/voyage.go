package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	voyageEndpoint     = "https://api.voyageai.com/v1/embeddings"
	voyageDefaultModel = "voyage-4-lite"
	defaultHTTPTimeout = 10 * time.Second
	voyageDimension    = 1024 // voyage-4-lite embedding dimension
)

// VoyageClient calls the VoyageAI embedding API.
type VoyageClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewVoyageClient creates a new VoyageAI embedding client.
// logger=nil falls back to slog.Default().
func NewVoyageClient(apiKey, model string, logger *slog.Logger) *VoyageClient {
	if model == "" {
		model = voyageDefaultModel
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &VoyageClient{
		apiKey: apiKey,
		model:  model,
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		logger: logger,
	}
}

type voyageRequest struct {
	Model     string   `json:"model"`
	Input     []string `json:"input"`
	InputType string   `json:"input_type"`
}

type voyageResponse struct {
	Data  []voyageEmbedding `json:"data"`
	Model string            `json:"model"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

type voyageEmbedding struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// Embed calls VoyageAI to embed one or more texts.
// Returns embeddings in the same order as input texts.
// Retries on 429/503 with exponential backoff.
func (v *VoyageClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	start := time.Now()
	outcome := outcomeSuccess
	defer func() {
		recordRequest("voyage", outcome, len(texts), time.Since(start))
	}()

	bodyBytes, err := json.Marshal(voyageRequest{
		Model:     v.model,
		Input:     texts,
		InputType: "query",
	})
	if err != nil {
		outcome = outcomeError
		return nil, fmt.Errorf("voyage: marshal request: %w", err)
	}

	embeddings, err := withRetry(ctx, defaultRetry, func() ([][]float32, int, error) {
		return v.doRequest(ctx, bodyBytes, texts)
	})
	if err != nil {
		outcome = outcomeError
	}
	return embeddings, err
}

// doRequest performs a single HTTP round-trip and parses the response. Returns
// (embeddings, httpStatus, error). Used by withRetry — non-retriable errors
// propagate immediately, retriable ones (429/5xx/timeouts) trigger backoff.
func (v *VoyageClient) doRequest(ctx context.Context, bodyBytes []byte, texts []string) ([][]float32, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, voyageEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("voyage: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.apiKey)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("voyage: http request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("voyage: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("voyage: status %d: %s", resp.StatusCode, string(respBody))
	}

	var voyResp voyageResponse
	if err := json.Unmarshal(respBody, &voyResp); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("voyage: unmarshal response: %w", err)
	}

	if len(voyResp.Data) != len(texts) {
		return nil, resp.StatusCode, fmt.Errorf("voyage: expected %d embeddings, got %d", len(texts), len(voyResp.Data))
	}

	out := make([][]float32, len(texts))
	for _, d := range voyResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, resp.StatusCode, fmt.Errorf("voyage: invalid embedding index %d", d.Index)
		}
		out[d.Index] = d.Embedding
	}

	v.logger.Debug("voyage embed complete",
		slog.Int("texts", len(texts)),
		slog.Int("tokens", voyResp.Usage.TotalTokens),
	)
	return out, resp.StatusCode, nil
}

// EmbedQuery embeds a single query string (search/retrieval use case).
// Delegates to Embed — VoyageAI already handles query vs document via input_type.
func (v *VoyageClient) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return EmbedQueryViaEmbed(ctx, v, text)
}

// Dimension returns the embedding vector dimension (1024 for voyage-4-lite).
func (v *VoyageClient) Dimension() int { return voyageDimension }

// Close is a no-op for the HTTP-based VoyageAI client.
func (v *VoyageClient) Close() error { return nil }

// Compile-time interface check.
var _ Embedder = (*VoyageClient)(nil)
