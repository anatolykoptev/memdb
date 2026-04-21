package search

// cross_encoder_rerank.go — best-effort cross-encoder reranker client.
//
// Speaks the **Cohere /v1/rerank de-facto standard**. Compatible with:
//   - Cohere hosted (https://api.cohere.com/v1/rerank, requires APIKey)
//   - Jina AI (https://api.jina.ai/v1/rerank, requires APIKey)
//   - Voyage AI (https://api.voyageai.com/v1/rerank, requires APIKey)
//   - Mixedbread AI (https://api.mixedbread.ai/v1/rerank, requires APIKey)
//   - HuggingFace text-embeddings-inference self-hosted
//   - Our embed-server self-hosted
//
// Request shape:  {model, query, documents: []string, top_n?}
// Response shape: {results: [{index, relevance_score}], ...}
//
// Auth: when APIKey is set, sends `Authorization: Bearer <key>` (works for
// every Cohere-compatible hosted provider). Self-hosted endpoints typically
// don't need it — leave APIKey empty.
//
// Runs BEFORE the LLM reranker in the search pipeline (step 6.05). Much cheaper
// than LLM rerank (~100-400ms vs ~3-4s) with strong cross-encoder relevance
// signal. On any error (network, HTTP !=2xx, decode) returns the input
// unchanged — this step is advisory, not authoritative.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// crossEncoderRespBodyLimit caps the response body read to avoid runaway
// allocations on a misbehaving server. A rerank response is small JSON; 256 KB
// covers pathological large top_n values.
const crossEncoderRespBodyLimit = 256 * 1024

// defaultCrossEncoderMaxDocs caps the documents actually shipped to
// embed-server when MaxDocs is unset. Protects against "100 results × 5K
// tokens" blowing up tokenization.
const defaultCrossEncoderMaxDocs = 50

// CrossEncoderConfig holds the settings for cross-encoder reranking.
// Zero-value URL disables the step silently.
type CrossEncoderConfig struct {
	URL     string        // base URL up to (excl.) /v1/rerank, e.g. "http://embed-server:8082" or "https://api.cohere.com"
	Model   string        // model name passed in request body, e.g. "gte-multi-rerank" or "rerank-multilingual-v3.0"
	APIKey  string        // when non-empty, sent as `Authorization: Bearer <key>`. Required for Cohere/Jina/Voyage hosted, optional for self-hosted.
	Timeout time.Duration // per-request HTTP timeout (propagated via ctx)
	MaxDocs int           // cap on documents sent to server (0 → default 50)
	// MaxCharsPerDoc caps doc length sent to the reranker. Cross-encoder
	// attention is O(seq²); avg memory rows are ~750 chars (≈300 tokens).
	// Truncating to ~200 chars (~80 tokens) gives a 4-9× speedup with
	// minimal quality loss — the leading content carries the bulk of the
	// query-relevance signal. Rune-aware (UTF-8 safe for Cyrillic).
	// 0 disables truncation.
	MaxCharsPerDoc int
}

// crossEncoderRequest mirrors embed-server's /v1/rerank input (Cohere-shaped).
type crossEncoderRequest struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      *int     `json:"top_n,omitempty"`
}

// crossEncoderResult is a single scored document from the rerank response.
type crossEncoderResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// crossEncoderResponse is the full rerank response shape.
type crossEncoderResponse struct {
	Model   string               `json:"model"`
	Results []crossEncoderResult `json:"results"`
}

// CrossEncoderRerank re-orders the first min(len(items), cfg.MaxDocs) entries
// of `items` by cross-encoder relevance to `query`. Items without the
// `textField` string value are kept in original order and NOT sent to the
// server. Items past the MaxDocs cap are preserved in original order after the
// reranked head (matches LLMRerank's "rerank top-N, append rest" behavior).
//
// Returns the input unchanged on any error — the pipeline must continue even
// when embed-server is unavailable.
func CrossEncoderRerank(
	ctx context.Context,
	query string,
	items []map[string]any,
	textField string,
	cfg CrossEncoderConfig,
) []map[string]any {
	if len(items) == 0 || cfg.URL == "" {
		return items
	}

	maxDocs := cfg.MaxDocs
	if maxDocs <= 0 {
		maxDocs = defaultCrossEncoderMaxDocs
	}

	// Split head (candidates for rerank) from tail (preserved as-is).
	head := items
	var tail []map[string]any
	if len(items) > maxDocs {
		head = items[:maxDocs]
		tail = items[maxDocs:]
	}

	// Collect docs and the original head indices that backed them.
	// Items missing the text field are skipped — they pass through unchanged.
	// MaxCharsPerDoc applied here (rune-aware) to bound the seq length the
	// reranker has to process — see CrossEncoderConfig docstring.
	docs := make([]string, 0, len(head))
	docIdxToHeadIdx := make([]int, 0, len(head))
	for i, item := range head {
		text, _ := item[textField].(string)
		if text == "" {
			continue
		}
		if cfg.MaxCharsPerDoc > 0 {
			text = truncateRunes(text, cfg.MaxCharsPerDoc)
		}
		docs = append(docs, text)
		docIdxToHeadIdx = append(docIdxToHeadIdx, i)
	}
	if len(docs) == 0 {
		return items
	}

	resp, err := callCrossEncoder(ctx, query, docs, cfg)
	if err != nil {
		slog.Default().Warn("cross_encoder rerank failed",
			slog.String("url", cfg.URL),
			slog.String("model", cfg.Model),
			slog.Int("docs", len(docs)),
			slog.Any("err", err),
		)
		return items
	}

	// Apply scores back onto the head items. Items skipped (no text field)
	// keep the sentinel score and sort to the bottom of the reranked block.
	const unscored = -1e18
	scores := make([]float64, len(head))
	for i := range scores {
		scores[i] = unscored
	}
	for _, r := range resp.Results {
		if r.Index < 0 || r.Index >= len(docIdxToHeadIdx) {
			continue // defensive: server returned out-of-range index
		}
		headIdx := docIdxToHeadIdx[r.Index]
		scores[headIdx] = r.RelevanceScore
		if meta, ok := head[headIdx]["metadata"].(map[string]any); ok {
			meta["relativity"] = r.RelevanceScore
			meta["cross_encoder_reranked"] = true
		}
	}

	// Sort indices by score descending (stable). Reorder head via the index
	// permutation so scores[i] always matches head[i] during comparison.
	order := make([]int, len(head))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return scores[order[i]] > scores[order[j]]
	})
	reordered := make([]map[string]any, len(head))
	for newPos, origIdx := range order {
		reordered[newPos] = head[origIdx]
	}

	if len(tail) == 0 {
		return reordered
	}
	// Preserve the tail (items beyond MaxDocs) after the reranked head.
	return append(reordered, tail...)
}

// callCrossEncoder POSTs the rerank request to embed-server and returns the
// parsed response. The caller's context deadline — plus cfg.Timeout — bounds
// the HTTP call.
func callCrossEncoder(
	ctx context.Context,
	query string,
	docs []string,
	cfg CrossEncoderConfig,
) (*crossEncoderResponse, error) {
	reqBody := crossEncoderRequest{
		Model:     cfg.Model,
		Query:     query,
		Documents: docs,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	callCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, cfg.URL+"/v1/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Bearer auth for Cohere-compatible hosted providers (Cohere, Jina,
	// Voyage, Mixedbread). Self-hosted reranker servers (TEI, embed-server)
	// usually need no auth — leave APIKey empty in that case.
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, crossEncoderRespBodyLimit))
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, crossEncoderRespBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var parsed crossEncoderResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &parsed, nil
}

// truncateRunes returns the first `maxRunes` runes of s. UTF-8 safe — won't
// split a multi-byte Cyrillic codepoint mid-sequence (which a naive `s[:N]`
// byte slice would). Returns s unchanged when it already fits.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}
