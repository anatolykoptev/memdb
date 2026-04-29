package embed

// OllamaClient — HTTP client for Ollama /api/embed (batch API, Ollama ≥ 0.3.6).
//
// # Compatibility with multilingual-e5-large
//
// Our reference ONNX embedder runs multilingual-e5-large WITHOUT any prefix —
// raw text goes directly into the model. The model was fine-tuned to work with
// "query: " / "passage: " prefixes for retrieval, but in our pipeline we store
// raw-text embeddings for consistency.
//
// Ollama models have Modelfile templates that auto-prepend prefixes:
//   - mxbai-embed-large  → "Represent this sentence for searching relevant passages: "
//   - nomic-embed-text   → "search_query: " (via SYSTEM template)
//
// Switching to Ollama with default settings WILL change the vector space and
// break cosine similarity against existing stored embeddings.
//
// # How to achieve 100% compatibility
//
// Option A (recommended): Use a model whose Modelfile has no prefix template.
//   - "jeffh/intfloat-multilingual-e5-large" on Ollama hub — same model as ONNX,
//     no prefix in its Modelfile. Vectors will be identical to ONNX output.
//   - Requires: WithOllamaDimension(1024) (default), no WithTextPrefix needed.
//
// Option B: Use mxbai-embed-large but create a custom Modelfile that removes
//   the prefix template:
//     FROM mxbai-embed-large
//     TEMPLATE "{{ .Prompt }}"
//   Then ollama create mxbai-noprefix -f Modelfile
//   Use model "mxbai-noprefix" + WithNormalizeL2(true).
//
// Option C: Use WithTextPrefix("") to send raw text, but Ollama will still
//   apply its Modelfile template server-side. This option does NOT bypass
//   the server-side template — it only controls what we prepend client-side.
//
// # Normalization
//
// Ollama ≥ 0.3.6 performs L2 normalization server-side (in llm/embed.go).
// Our reference ONNX embedder also L2-normalizes. Both produce unit vectors
// → cosine similarity = dot product. WithNormalizeL2 is available as a safety
// net for older Ollama versions or models that don't normalize.

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

const (
	ollamaDefaultURL     = "http://localhost:11434"
	ollamaEmbedPath      = "/api/embed"
	ollamaDefaultModel   = "nomic-embed-text"
	ollamaDefaultDim     = 1024
	ollamaDefaultTimeout = 60 * time.Second
)

// OllamaClient calls the Ollama /api/embed endpoint.
// Supports batch embedding (multiple texts in one request).
// No CGO, no ONNX Runtime — pure HTTP client.
// Compatible with Ollama ≥ 0.3.6 which introduced the batch /api/embed endpoint.
type OllamaClient struct {
	baseURL     string
	model       string
	dim         int
	detectedDim int    // auto-detected from first response; 0 = not yet seen
	textPrefix  string // prepended client-side to every document text before sending
	queryPrefix string // prepended client-side to query text (EmbedQuery)
	normalizeL2 bool   // apply L2 normalization client-side after receiving embeddings
	httpClient  *http.Client
	logger      *slog.Logger
}

// OllamaOption is a functional option for OllamaClient.
type OllamaOption func(*OllamaClient)

// WithOllamaDimension overrides the reported embedding dimension.
// The default is 1024 to match the existing pgvector/Qdrant schema (vector(1024)).
// Use this only if deploying a model with a different dimension.
func WithOllamaDimension(dim int) OllamaOption {
	return func(c *OllamaClient) { c.dim = dim }
}

// WithOllamaTimeout overrides the HTTP client timeout (default 60s).
// Increase for large batches or slow hardware.
func WithOllamaTimeout(d time.Duration) OllamaOption {
	return func(c *OllamaClient) { c.httpClient.Timeout = d }
}

// WithTextPrefix sets a string prepended client-side to every document text
// before sending to Ollama (used by Embed). Separate from Ollama's server-side
// Modelfile template.
//
// Example: WithTextPrefix("passage: ") for e5-style document storage.
// Default: "" (no prefix — raw text, compatible with existing ONNX vectors).
func WithTextPrefix(prefix string) OllamaOption {
	return func(c *OllamaClient) { c.textPrefix = prefix }
}

// WithQueryPrefix sets a string prepended client-side to query text in EmbedQuery.
// Allows different prefixes for storage (Embed) vs retrieval (EmbedQuery).
//
// Example: WithQueryPrefix("query: ") for e5-style retrieval.
// Default: "" (same as document prefix — no distinction).
func WithQueryPrefix(prefix string) OllamaOption {
	return func(c *OllamaClient) { c.queryPrefix = prefix }
}

// WithNormalizeL2 enables client-side L2 normalization of embeddings.
// Ollama ≥ 0.3.6 already normalizes server-side, so this is a no-op in most cases.
// Enable only if using an older Ollama version or a model that does not normalize.
func WithNormalizeL2(enabled bool) OllamaOption {
	return func(c *OllamaClient) { c.normalizeL2 = enabled }
}

// NewOllamaClient creates a new Ollama embedding client.
// baseURL: Ollama server URL (e.g. "http://localhost:11434"), empty = default.
// model: embedding model name (e.g. "nomic-embed-text", "mxbai-embed-large"), empty = default.
// logger=nil falls back to slog.Default().
func NewOllamaClient(baseURL, model string, logger *slog.Logger, opts ...OllamaOption) *OllamaClient {
	if baseURL == "" {
		baseURL = ollamaDefaultURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		model = ollamaDefaultModel
	}
	if logger == nil {
		logger = slog.Default()
	}
	c := &OllamaClient{
		baseURL: baseURL,
		model:   model,
		dim:     ollamaDefaultDim,
		httpClient: &http.Client{
			Timeout: ollamaDefaultTimeout,
		},
		logger: logger,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ollamaEmbedRequest is the request body for POST /api/embed.
// Ollama ≥ 0.3.6: input accepts a list of strings for batch embedding.
type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
	// KeepAlive controls how long the model stays loaded (default "5m").
	// Set to "0" to unload immediately after embedding.
	KeepAlive string `json:"keep_alive,omitempty"`
}

// ollamaEmbedResponse is the response from POST /api/embed.
type ollamaEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float32 `json:"embeddings"`
}

// embedRaw sends input strings as-is to /api/embed (no prefix applied).
// Shared by Embed and EmbedQuery. Applies normalizeL2, updates detectedDim,
// and retries on transient errors (429, 503, timeouts) with exponential backoff.
func (c *OllamaClient) embedRaw(ctx context.Context, input []string) ([][]float32, error) {
	reqBody := ollamaEmbedRequest{
		Model: c.model,
		Input: input,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	url := c.baseURL + ollamaEmbedPath

	result, err := withRetry(ctx, defaultRetry, func() ([][]float32, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, 0, fmt.Errorf("ollama: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("ollama: http request to %s: %w", url, err)
		}
		defer resp.Body.Close() //nolint:errcheck

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("ollama: read response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, resp.StatusCode, fmt.Errorf("ollama: status %d: %s", resp.StatusCode, string(respBody))
		}

		var ollamaResp ollamaEmbedResponse
		if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
			return nil, resp.StatusCode, fmt.Errorf("ollama: unmarshal response: %w", err)
		}

		if len(ollamaResp.Embeddings) != len(input) {
			return nil, resp.StatusCode, fmt.Errorf("ollama: expected %d embeddings, got %d", len(input), len(ollamaResp.Embeddings))
		}

		return ollamaResp.Embeddings, resp.StatusCode, nil
	})
	if err != nil {
		return nil, err
	}

	if c.normalizeL2 {
		for i := range result {
			l2Normalize(result[i])
		}
	}

	if c.detectedDim == 0 && len(result[0]) > 0 {
		c.detectedDim = len(result[0])
	}

	return result, nil
}

// Embed calls Ollama /api/embed to embed one or more texts (document/storage use case).
// Applies WithTextPrefix client-side before sending.
// Returns embeddings in the same order as input texts. Empty input returns nil, nil.
func (c *OllamaClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	start := time.Now()
	outcome := outcomeSuccess
	defer func() {
		recordRequest("ollama", outcome, len(texts), time.Since(start))
	}()

	input := texts
	if c.textPrefix != "" {
		input = make([]string, len(texts))
		for i, t := range texts {
			input[i] = c.textPrefix + t
		}
	}
	embs, err := c.embedRaw(ctx, input)
	if err != nil {
		outcome = outcomeError
		return nil, err
	}
	c.logger.Debug("ollama embed complete",
		slog.String("model", c.model),
		slog.Int("texts", len(texts)),
		slog.Int("dim", c.detectedDim),
	)
	return embs, nil
}

// EmbedQuery embeds a single query string (search/retrieval use case).
// Applies WithQueryPrefix if set, otherwise falls back to WithTextPrefix.
func (c *OllamaClient) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	input := text
	switch {
	case c.queryPrefix != "":
		input = c.queryPrefix + text
	case c.textPrefix != "":
		input = c.textPrefix + text
	}
	vecs, err := c.embedRaw(ctx, []string{input})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, nil
	}
	return vecs[0], nil
}

// Dimension returns the embedding vector dimension.
// Returns the auto-detected dimension from the first response if available,
// otherwise the configured default (1024). Override with WithOllamaDimension.
func (c *OllamaClient) Dimension() int {
	if c.detectedDim > 0 {
		return c.detectedDim
	}
	return c.dim
}

// Close is a no-op for the HTTP-based Ollama client.
func (c *OllamaClient) Close() error { return nil }

// Compile-time interface check.
var _ Embedder = (*OllamaClient)(nil)
