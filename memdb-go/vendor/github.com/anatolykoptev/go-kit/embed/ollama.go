package embed

// OllamaClient — HTTP client for Ollama /api/embed (batch API, Ollama ≥ 0.3.6).
//
// # Embedder vs storage convention — two separate layers
//
// The embedder (ONNX runtime or Ollama /api/embed) accepts raw text with no
// prefix — the model takes whatever bytes it is given. That is a property of
// the runtime, NOT of the corpus.
//
// The *storage convention* for e5-family corpora (multilingual-e5-large and
// siblings) is the opposite: documents are embedded with the "passage: "
// prefix and queries with the "query: " prefix. The model was fine-tuned to
// expect these for retrieval, and the existing resume_vectors / algora corpora
// in go-job were embedded with "passage: ". A caller that follows the old
// wording of this comment ("we store raw-text embeddings") writes vectors into
// a different region of the space — cosine ~0.97 against the stored ones,
// high enough to look fine and low enough to reorder results. See issue #259.
//
// Use the exported [E5PassagePrefix] / [E5QueryPrefix] constants rather than
// re-typing the string literals: the contract is checkable, not remembered.
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
//     no prefix in its Modelfile. Vectors will be identical to ONNX output
//     when the SAME client-side prefix convention is applied (see above).
//   - Requires: WithOllamaDimension(1024) (default), WithTextPrefix(E5PassagePrefix)
//     + WithQueryPrefix(E5QueryPrefix) for e5 corpora.
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

// E5PassagePrefix is the e5-family storage convention for documents
// (multilingual-e5-large and siblings). The embedder itself accepts raw
// text; this prefix is the *pipeline* convention the existing corpora
// (resume_vectors, algora) were embedded with. Use it via
// WithTextPrefix(E5PassagePrefix) when writing documents that must be
// retrievable against those corpora. See issue #259.
const E5PassagePrefix = "passage: "

// E5QueryPrefix is the e5-family retrieval convention for queries. Use it
// via WithQueryPrefix(E5QueryPrefix) so the query side of retrieval sees
// the prefix the model was fine-tuned on. Paired with E5PassagePrefix on
// the storage side; mixing the two (e.g. query prefix on a stored
// document) silently degrades retrieval — see TestOllamaClient_E5PrefixesNotInterchangeable.
const E5QueryPrefix = "query: "

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
	// e5-family models are instruction-tuned for "query: "/"passage: " prefixes.
	// A missing prefix does not error — retrieval just quietly degrades (cosine
	// ~0.97 vs 1.0, enough to reorder results). Warn so the silent-degradation
	// class from issue #259 surfaces at construction, not after a corpus is
	// written. The caller may still intentionally use raw text (e.g. for a
	// non-e5 corpus or a custom Modelfile that strips the template); the warning
	// is informational, not a hard error.
	if isE5Model(model) && c.textPrefix == "" && c.queryPrefix == "" {
		c.logger.Warn("ollama embedder: model name suggests the e5 family, which requires "+
			"\"query: \"/\"passage: \" prefixes for retrieval quality — set "+
			"WithTextPrefix(E5PassagePrefix) and WithQueryPrefix(E5QueryPrefix), "+
			"or retrieval against e5 corpora will look plausible but quietly degrade",
			slog.String("model", model),
		)
	}
	return c
}

// isE5Model reports whether model name hints at the e5 family
// (intfloat/multilingual-e5-large and siblings). Match is case-insensitive
// substring so registry-qualified names (e.g. "jeffh/intfloat-multilingual-e5-large")
// and Ollama tags (e.g. "multilingual-e5-large:latest") are handled.
func isE5Model(model string) bool {
	return strings.Contains(strings.ToLower(model), "e5")
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
		defer func() {
			if err := resp.Body.Close(); err != nil {
				slog.Warn("ollama: response body close failed",
					slog.String("url", url),
					slog.Any("error", err),
				)
			}
		}()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, resp.StatusCode, fmt.Errorf("ollama: read response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, resp.StatusCode, fmt.Errorf("ollama embedder: %w", &errHTTPStatus{Code: resp.StatusCode, Body: string(respBody)})
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

// MaxBatchSize returns the maximum recommended batch size for Ollama's
// /api/embed endpoint. Ollama has no hard documented limit, but large batches
// can timeout on slow hardware; 32 is a safe default that matches the
// ox-embed-server cap and keeps latency bounded.
func (c *OllamaClient) MaxBatchSize() int { return ollamaMaxBatch }

// ollamaMaxBatch is the recommended batch size for Ollama /api/embed.
const ollamaMaxBatch = 32

// Compile-time interface checks.
var (
	_ Embedder   = (*OllamaClient)(nil)
	_ BatchSizer = (*OllamaClient)(nil)
)
