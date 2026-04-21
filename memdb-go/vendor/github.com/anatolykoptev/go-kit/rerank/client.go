package rerank

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// defaultMaxDocs caps docs shipped to the server when Config.MaxDocs is 0.
const defaultMaxDocs = 50

// respBodyLimit bounds response body read to avoid runaway allocations on a
// misbehaving server. Rerank responses are small JSON; 256 KB covers
// pathological top_n values.
const respBodyLimit = 256 * 1024

// Config configures a rerank client. Zero URL disables all calls.
type Config struct {
	URL            string        // base URL, e.g. "http://embed-server:8082"
	Model          string        // model name in request body
	APIKey         string        // optional Bearer token (Cohere hosted providers)
	Timeout        time.Duration // per-request HTTP timeout (applied via context.WithTimeout, NOT http.Client.Timeout)
	MaxDocs        int           // cap on docs sent (0 → defaultMaxDocs)
	MaxCharsPerDoc int           // rune-aware truncation (0 disables)
}

// Doc is a query-document pair. ID is opaque, returned unchanged in Scored.
type Doc struct {
	ID   string
	Text string
}

// Scored pairs an input Doc with its relevance score from the reranker.
// OrigRank is the original index of this doc in the input slice.
type Scored struct {
	Doc
	Score    float32
	OrigRank int
}

// Client is the rerank HTTP client. Safe for concurrent use.
type Client struct {
	cfg    Config
	logger *slog.Logger
	hc     *http.Client
}

// New returns a configured client. logger=nil uses slog.Default().
func New(cfg Config, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		cfg:    cfg,
		logger: logger,
		hc:     &http.Client{},
	}
}

// Available reports whether the client is configured to make calls.
func (c *Client) Available() bool {
	return c != nil && c.cfg.URL != ""
}

// Rerank returns docs sorted by cross-encoder relevance score (desc). Best-
// effort: any error returns input unchanged (preserving order, Score=0,
// OrigRank=i). Docs beyond MaxDocs are preserved as-is after the reranked
// head.
func (c *Client) Rerank(ctx context.Context, query string, docs []Doc) []Scored {
	// Always build Scored output, even on fast-path exits, so callers can
	// always index .Score and .OrigRank without nil checks.
	pass := func(reason string) []Scored {
		out := make([]Scored, len(docs))
		for i, d := range docs {
			out[i] = Scored{Doc: d, OrigRank: i}
		}
		if reason != "" && c != nil {
			recordStatus(c.cfg.Model, reason)
		}
		return out
	}
	if len(docs) == 0 || c == nil || c.cfg.URL == "" {
		return pass("")
	}

	maxDocs := c.cfg.MaxDocs
	if maxDocs <= 0 {
		maxDocs = defaultMaxDocs
	}

	head := docs
	var tail []Doc
	if len(docs) > maxDocs {
		head = docs[:maxDocs]
		tail = docs[maxDocs:]
	}

	// Extract texts (with optional rune-aware truncation).
	texts := make([]string, len(head))
	for i, d := range head {
		t := d.Text
		if c.cfg.MaxCharsPerDoc > 0 {
			t = truncateRunes(t, c.cfg.MaxCharsPerDoc)
		}
		texts[i] = t
	}

	start := time.Now()
	resp, err := c.callCohere(ctx, query, texts)
	recordDuration(c.cfg.Model, time.Since(start))
	if err != nil {
		c.logger.Warn("rerank failed",
			slog.String("url", c.cfg.URL),
			slog.String("model", c.cfg.Model),
			slog.Int("docs", len(texts)),
			slog.Any("err", err),
		)
		recordStatus(c.cfg.Model, "error")
		return pass("")
	}
	recordStatus(c.cfg.Model, "ok")

	// Build scored head in server-returned order. Missing docs keep score=0
	// and get sorted to tail of the head block.
	scores := make([]float32, len(head))
	seen := make([]bool, len(head))
	for _, r := range resp.Results {
		if r.Index < 0 || r.Index >= len(head) {
			continue // defensive
		}
		scores[r.Index] = float32(r.RelevanceScore)
		seen[r.Index] = true
	}

	// Sort indices by score desc (stable). Sort through a permutation so the
	// comparator reads from a stable scores array — NOT from shuffled items.
	order := make([]int, len(head))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		// Unseen docs go to the end of the reranked block.
		if seen[order[i]] != seen[order[j]] {
			return seen[order[i]]
		}
		return scores[order[i]] > scores[order[j]]
	})

	out := make([]Scored, 0, len(docs))
	for _, origIdx := range order {
		out = append(out, Scored{
			Doc:      head[origIdx],
			Score:    scores[origIdx],
			OrigRank: origIdx,
		})
	}
	// Preserve tail in original order at the end.
	for i, d := range tail {
		out = append(out, Scored{
			Doc:      d,
			Score:    0,
			OrigRank: maxDocs + i,
		})
	}
	return out
}

// truncateRunes returns the first maxRunes runes of s. UTF-8 safe.
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
