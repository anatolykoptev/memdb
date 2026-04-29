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
	Normalize string   `json:"normalize,omitempty"` // G2-client: server-side normalize mode; "" omitted (Cohere compat)
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

// errHTTPStatus is a typed error carrying the HTTP status code from a non-2xx
// response. Using a typed error (rather than a plain fmt.Errorf string) allows
// retry.do to type-assert the code for RetryableStatus filtering without
// parsing the error string.
//
// The Error() string is "http status <code>" — identical to the previous
// fmt.Errorf("http status %d", ...) format, preserving backward compatibility
// for any caller that does strings.Contains(err.Error(), "http status").
type errHTTPStatus struct {
	Code int
}

func (e errHTTPStatus) Error() string {
	return fmt.Sprintf("http status %d", e.Code)
}

// callCohere POSTs the rerank request and returns the parsed response.
// The caller's ctx plus c.cfg.timeout bounds the HTTP call.
func (c *Client) callCohere(ctx context.Context, query string, docs []string) (*cohereResponse, error) {
	body, err := json.Marshal(cohereRequest{
		Model:     c.cfg.model,
		Query:     query,
		Documents: docs,
		Normalize: c.cfg.serverNormalize, // "" → omitempty → field absent (Cohere compat)
	})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	callCtx := ctx
	if c.cfg.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.cfg.timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.cfg.url+"/v1/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.apiKey)
	}

	resp, err := c.cfg.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, respBodyLimit))
		return nil, errHTTPStatus{Code: resp.StatusCode}
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
