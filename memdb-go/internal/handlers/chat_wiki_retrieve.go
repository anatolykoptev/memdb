package handlers

// chat_wiki_retrieve.go — W3.5 wiki-as-retrieval-slot.
//
// Architecturally distinct from chat_wiki_inject.go (W3 prefix path):
//
//	prefix path  → wiki block prepended to system prompt unconditionally
//	slot path    → wiki hits formatted as memory entries, merged into
//	               the ranked memory list, sortByRelativity decides whether
//	               they survive the threshold filter
//
// Inspired by graphiti's `community_search` (graphiti_core/search/search.py)
// which surfaces community summaries through the same RRF pipeline as nodes
// and edges. Our shape is simpler — one ranked memory list with a relativity
// score — but the principle is identical: a synthesized artifact earns its
// place in the prompt by ranking, not by being mandatory.
//
// Two gates can run together (A/B):
//
//	MEMDB_WIKI_SEARCH_INJECT     — prefix path (chat_wiki_inject.go)
//	MEMDB_WIKI_RETRIEVAL_SLOT    — slot path (this file)
//
// Both default OFF.

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

const (
	wikiRetrievalSlotEnv          = "MEMDB_WIKI_RETRIEVAL_SLOT"
	wikiRetrievalTopKEnv          = "MEMDB_WIKI_RETRIEVAL_TOP_K"
	wikiRetrievalMaxBodyTokensEnv = "MEMDB_WIKI_RETRIEVAL_MAX_BODY_TOKENS"
	wikiRetrievalMinScoreEnv      = "MEMDB_WIKI_RETRIEVAL_MIN_SCORE"

	defaultWikiRetrievalTopK          = 3
	defaultWikiRetrievalMaxBodyTokens = 200
	defaultWikiRetrievalMinScore      = 0.30 // matches chat threshold default

	wikiRetrievalTopKMin          = 1
	wikiRetrievalTopKMax          = 10
	wikiRetrievalMaxBodyTokensMin = 50
	wikiRetrievalMaxBodyTokensMax = 1000

	// wikiRetrievalMemoryType labels wiki entries inside the merged memory
	// list. memType() returns this so downstream logging / debugging can
	// tell wiki hits apart from real LTM/UserMemory rows.
	wikiRetrievalMemoryType = "WikiPage"
)

func wikiRetrievalSlotEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(wikiRetrievalSlotEnv)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func wikiRetrievalTopK() int {
	raw := strings.TrimSpace(os.Getenv(wikiRetrievalTopKEnv))
	if raw == "" {
		return defaultWikiRetrievalTopK
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultWikiRetrievalTopK
	}
	if n < wikiRetrievalTopKMin {
		return wikiRetrievalTopKMin
	}
	if n > wikiRetrievalTopKMax {
		return wikiRetrievalTopKMax
	}
	return n
}

func wikiRetrievalMaxBodyTokens() int {
	raw := strings.TrimSpace(os.Getenv(wikiRetrievalMaxBodyTokensEnv))
	if raw == "" {
		return defaultWikiRetrievalMaxBodyTokens
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultWikiRetrievalMaxBodyTokens
	}
	if n < wikiRetrievalMaxBodyTokensMin {
		return wikiRetrievalMaxBodyTokensMin
	}
	if n > wikiRetrievalMaxBodyTokensMax {
		return wikiRetrievalMaxBodyTokensMax
	}
	return n
}

// wikiRetrievalMinScore is the minimum cosine-similarity for a wiki page to
// be merged into the memory list. Equals the chat threshold default (0.30)
// so wiki pages don't sneak in below what real memories would survive.
func wikiRetrievalMinScore() float64 {
	raw := strings.TrimSpace(os.Getenv(wikiRetrievalMinScoreEnv))
	if raw == "" {
		return defaultWikiRetrievalMinScore
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return defaultWikiRetrievalMinScore
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// fetchWikiAsMemories embeds the query, cosine-searches wiki_pages, and
// renders matching pages as memory-shaped maps ready to merge into the
// chatSearchMemories result.
//
// Each wiki entry produced has the same map shape as a real memory:
//
//	{
//	  "id":       "wiki:" + slug,
//	  "memory":   "[Wiki] <title>: <body>",
//	  "metadata": {
//	     "relativity":  <score 0..1>,
//	     "memory_type": "WikiPage",
//	  },
//	}
//
// Returns nil on any of: gate off, empty handles, embed/search error,
// no hits above min-score. All failure paths log warn-only.
func (h *Handler) fetchWikiAsMemories(ctx context.Context, cubeID, query string) []map[string]any {
	if !wikiRetrievalSlotEnabled() {
		recordWikiSlotOutcome(ctx, wikiSlotOutcomeGateOff)
		return nil
	}
	if h.postgres == nil || h.embedder == nil {
		recordWikiSlotOutcome(ctx, wikiSlotOutcomeNoHandles)
		return nil
	}
	if cubeID == "" || strings.TrimSpace(query) == "" {
		recordWikiSlotOutcome(ctx, wikiSlotOutcomeNoQuery)
		return nil
	}
	vecs, err := h.embedder.Embed(ctx, []string{query})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		if err != nil {
			h.logger.Warn("wiki slot: embed failed",
				slog.String("cube_id", cubeID),
				slog.Any("error", err))
		}
		recordWikiSlotOutcome(ctx, wikiSlotOutcomeEmbedError)
		return nil
	}
	hits, err := h.postgres.SearchWikiByCosine(ctx, cubeID, vecs[0], wikiRetrievalTopK())
	if err != nil {
		h.logger.Warn("wiki slot: search failed",
			slog.String("cube_id", cubeID),
			slog.Any("error", err))
		recordWikiSlotOutcome(ctx, wikiSlotOutcomeSearchError)
		return nil
	}
	if len(hits) == 0 {
		recordWikiSlotOutcome(ctx, wikiSlotOutcomeNoResults)
		return nil
	}

	minScore := wikiRetrievalMinScore()
	maxBody := wikiRetrievalMaxBodyTokens()
	out := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		if hit.Score < minScore {
			continue
		}
		view := WikiSearchHitView{
			Slug:  hit.Page.Slug,
			Title: hit.Page.Title,
			Body:  hit.Page.Body,
			Score: hit.Score,
		}
		out = append(out, formatWikiAsMemory(view, maxBody))
	}
	if len(out) == 0 {
		recordWikiSlotOutcome(ctx, wikiSlotOutcomeBelowMinScore)
		return nil
	}
	recordWikiSlotOutcome(ctx, wikiSlotOutcomeMerged)
	recordWikiSlotPagesMerged(ctx, len(out))
	return out
}

// formatWikiAsMemory shapes one WikiSearchHit into the memory-map shape
// chatSearchMemories returns. The text is "[Wiki] <title>: <truncated-body>";
// the [Wiki] prefix gives the LLM a hint that this content is a synthesis
// rather than a verbatim memory, which matters for citation-style answers
// in factual mode.
func formatWikiAsMemory(hit WikiSearchHitView, maxBodyTokens int) map[string]any {
	title := strings.TrimSpace(hit.Title)
	if title == "" {
		title = hit.Slug
	}
	body := truncateMarkdown(strings.TrimSpace(hit.Body), maxBodyTokens)
	text := "[Wiki] " + title
	if body != "" {
		text += ": " + body
	}
	return map[string]any{
		"id":     "wiki:" + hit.Slug,
		"memory": text,
		"metadata": map[string]any{
			"relativity":  hit.Score,
			"memory_type": wikiRetrievalMemoryType,
		},
	}
}

// WikiSearchHitView is the view of a wiki hit formatWikiAsMemory needs —
// kept narrow so the helper can be tested without constructing a full
// db.WikiSearchHit (with its embedded WikiPage and pgx-tied timestamps).
type WikiSearchHitView struct {
	Slug  string
	Title string
	Body  string
	Score float64
}
