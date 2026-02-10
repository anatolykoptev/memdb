// Package embedder provides clients for embedding APIs.
package embedder

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
	voyageEndpoint    = "https://api.voyageai.com/v1/embeddings"
	defaultModel      = "voyage-4-lite"
	defaultHTTPTimeout = 10 * time.Second
)

// VoyageClient calls the VoyageAI embedding API.
type VoyageClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewVoyageClient creates a new VoyageAI embedding client.
func NewVoyageClient(apiKey, model string, logger *slog.Logger) *VoyageClient {
	if model == "" {
		model = defaultModel
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
func (v *VoyageClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := voyageRequest{
		Model:     v.model,
		Input:     texts,
		InputType: "query",
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("voyage: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, voyageEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("voyage: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.apiKey)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voyage: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("voyage: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("voyage: status %d: %s", resp.StatusCode, string(respBody))
	}

	var voyResp voyageResponse
	if err := json.Unmarshal(respBody, &voyResp); err != nil {
		return nil, fmt.Errorf("voyage: unmarshal response: %w", err)
	}

	if len(voyResp.Data) != len(texts) {
		return nil, fmt.Errorf("voyage: expected %d embeddings, got %d", len(texts), len(voyResp.Data))
	}

	// Sort by index to ensure correct order
	embeddings := make([][]float32, len(texts))
	for _, d := range voyResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("voyage: invalid embedding index %d", d.Index)
		}
		embeddings[d.Index] = d.Embedding
	}

	v.logger.Debug("voyage embed complete",
		slog.Int("texts", len(texts)),
		slog.Int("tokens", voyResp.Usage.TotalTokens),
	)

	return embeddings, nil
}
