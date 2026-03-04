// Package search — SearXNG internet search client with graceful degradation.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const internetTimeout = 10 * time.Second

// InternetResult represents a single search result from SearXNG.
type InternetResult struct {
	Title   string
	Content string
	URL     string
}

// Text returns a human-readable representation: "Title: Content".
func (r InternetResult) Text() string {
	return r.Title + ": " + r.Content
}

// InternetSearcher queries SearXNG for web search results.
type InternetSearcher struct {
	baseURL string
	limit   int
	client  http.Client
}

// NewInternetSearcher creates a searcher targeting the given SearXNG base URL.
func NewInternetSearcher(baseURL string, limit int) *InternetSearcher {
	return &InternetSearcher{
		baseURL: baseURL,
		limit:   limit,
		client:  http.Client{Timeout: internetTimeout},
	}
}

// searxngResponse is the top-level JSON envelope from SearXNG.
type searxngResponse struct {
	Results []searxngResult `json:"results"`
}

// searxngResult is a single item in the SearXNG results array.
type searxngResult struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	URL     string `json:"url"`
}

// Search queries SearXNG and returns up to s.limit results.
// On any failure (network, bad status, parse error) it returns an empty slice
// instead of an error, providing graceful degradation.
func (s *InternetSearcher) Search(ctx context.Context, query string) ([]InternetResult, error) {
	reqURL := fmt.Sprintf("%s/search?q=%s&format=json&categories=general",
		s.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return []InternetResult{}, nil
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return []InternetResult{}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return []InternetResult{}, nil
	}

	var body searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return []InternetResult{}, nil
	}

	results := make([]InternetResult, 0, min(len(body.Results), s.limit))
	for _, r := range body.Results {
		if len(results) >= s.limit {
			break
		}
		if r.Title == "" && r.Content == "" {
			continue
		}
		results = append(results, InternetResult{
			Title:   r.Title,
			Content: r.Content,
			URL:     r.URL,
		})
	}

	return results, nil
}
