// Package sources provides specialized search source integrations.
//
// Each source wraps an external API or scraper: HackerNews (Algolia),
// YouTube (InnerTube), Context7, HuggingFace, WordPress dev docs,
// and GitHub URL utilities.
//
// All sources implement the [Source] interface. Construct queries with
// [Query] and collect results as [Result] slices.
package sources

import "context"

// Source is the interface implemented by all search source integrations.
//
// Name returns the unique identifier for the source (e.g. "hackernews", "youtube").
// Search executes a search against the source and returns a ranked or unranked
// list of results. The caller should pass a context to support cancellation.
type Source interface {
	// Name returns the unique identifier of this source.
	Name() string
	// Search performs a query against the source.
	// Returns results in the source's natural order; Score may be 0 (unranked).
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Query describes a search request sent to a [Source].
//
// TimeRange filters results by recency:
//   - "" — no filter (all time)
//   - "day" — past 24 hours
//   - "week" — past 7 days
//   - "month" — past 30 days
//   - "year" — past 365 days
//
// Language is an ISO 639-1 two-letter code (e.g. "en", "ru").
// Extra carries source-specific parameters not covered by the standard fields.
type Query struct {
	// Text is the search text sent to the source.
	Text string
	// Limit is the maximum number of results to return (0 means source default).
	Limit int
	// TimeRange filters results by recency. See package-level documentation.
	TimeRange string
	// Language restricts results to the given ISO 639-1 language code.
	Language string
	// Extra holds source-specific key/value parameters.
	Extra map[string]string
}

// Result is a single item returned by a [Source].
//
// Score is a relevance score in [0, 1]. A value of 0 means the source did
// not provide ranking information. Callers must not rely on Score being
// comparable across different sources.
//
// Metadata carries source-specific fields (e.g. "points", "author", "channel").
type Result struct {
	// Title is the human-readable title of the result.
	Title string
	// URL is the canonical link to the result.
	URL string
	// Content is a snippet or full text of the result.
	Content string
	// Score is a relevance score in [0, 1], or 0 if unranked.
	Score float64
	// Metadata holds source-specific key/value attributes.
	Metadata map[string]string
}
