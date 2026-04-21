package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// cohereRequest mirrors the Cohere /v1/rerank request body (also accepted by
// embed-server, TEI, Jina, Voyage, Mixedbread).
type cohereRequest struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      *int     `json:"top_n,omitempty"`
}

// cohereResult is a single scored doc in the rerank response.
type cohereResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// cohereResponse is the full rerank response body.
type cohereResponse struct {
	Model   string         `json:"model"`
	Results []cohereResult `json:"results"`
}

// callCohere POSTs the rerank request and returns the parsed response.
// The caller's ctx plus c.cfg.Timeout bounds the HTTP call.
func (c *Client) callCohere(ctx context.Context, query string, docs []string) (*cohereResponse, error) {
	body, err := json.Marshal(cohereRequest{
		Model:     c.cfg.Model,
		Query:     query,
		Documents: docs,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	callCtx := ctx
	if c.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.cfg.URL+"/v1/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, respBodyLimit))
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	rb, err := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var parsed cohereResponse
	if err := json.Unmarshal(rb, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &parsed, nil
}
