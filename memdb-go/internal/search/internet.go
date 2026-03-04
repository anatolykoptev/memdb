// Package search — internet search via go-engine (SearXNG + DDG/Startpage + WRR fusion).
package search

import (
	"context"
	"log/slog"

	enginesearch "github.com/anatolykoptev/go-engine/search"
	"github.com/anatolykoptev/go-engine/sources"
)

// InternetResult represents a single search result.
type InternetResult struct {
	Title   string
	Content string
	URL     string
}

// Text returns a human-readable representation: "Title: Content".
func (r InternetResult) Text() string {
	return r.Title + ": " + r.Content
}

// InternetSearcherConfig holds configuration for creating an InternetSearcher.
type InternetSearcherConfig struct {
	SearXNGURL string
	Limit      int
	Browser    enginesearch.BrowserDoer // nil = SearXNG-only mode
}

// InternetSearcher queries SearXNG and optional direct scrapers (DDG/Startpage).
type InternetSearcher struct {
	searxng *enginesearch.SearXNG
	browser enginesearch.BrowserDoer
	limit   int
}

// NewInternetSearcher creates a searcher with go-engine SearXNG client and optional direct scrapers.
func NewInternetSearcher(cfg InternetSearcherConfig) *InternetSearcher {
	return &InternetSearcher{
		searxng: enginesearch.NewSearXNG(cfg.SearXNGURL),
		browser: cfg.Browser,
		limit:   cfg.Limit,
	}
}

// Search queries SearXNG, optionally DDG/Startpage via proxy, fuses results with WRR,
// and returns up to s.limit results. On any failure it returns an empty slice (graceful degradation).
func (s *InternetSearcher) Search(ctx context.Context, query string) ([]InternetResult, error) {
	// 1. Query SearXNG.
	searxngResults, err := s.searxng.Search(ctx, query, "", "", "")
	if err != nil {
		slog.Debug("searxng search failed", slog.Any("error", err))
		searxngResults = nil
	}

	// 2. Query direct scrapers (DDG + Startpage) if browser client is available.
	var directResults []sources.Result
	if s.browser != nil {
		directResults = enginesearch.SearchDirect(ctx, enginesearch.DirectConfig{
			Browser:   s.browser,
			DDG:       true,
			Startpage: true,
		}, query, "")
	}

	// 3. Fuse results with WRR (SearXNG weight=1.0, direct=1.2).
	fused := enginesearch.FuseWRR(
		[][]sources.Result{searxngResults, directResults},
		[]float64{1.0, 1.2},
	)

	// 4. Convert to InternetResult and limit.
	results := make([]InternetResult, 0, min(len(fused), s.limit))
	for _, r := range fused {
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
