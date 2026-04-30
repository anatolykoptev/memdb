package handlers

// chat_wiki_inject.go — pre-compiled wiki summaries injected into the chat
// system prompt before the per-turn memory block.
//
// Karpathy LLM Wiki "compounding artifact": each tree-reorg promotion writes
// a summary to memos_graph.wiki_pages (see scheduler/reorganizer_wiki.go).
// At chat time we cosine-search those pages by query and prepend the top-K
// bodies as a "## Wiki Synthesis" block so the model sees pre-synthesized
// context alongside raw retrieved memories.
//
// Env-gated by MEMDB_WIKI_SEARCH_INJECT (default OFF) so existing deployments
// don't change behavior on upgrade. The block flows through the standard
// basePrompt path → buildSystemPromptWithBudget will auto-trim if overall
// prompt exceeds max_context_tokens, no separate accounting needed.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

const (
	wikiSearchInjectEnv        = "MEMDB_WIKI_SEARCH_INJECT"
	wikiInjectTopKEnv          = "MEMDB_WIKI_INJECT_TOP_K"
	wikiInjectMaxBodyTokensEnv = "MEMDB_WIKI_INJECT_MAX_BODY_TOKENS"

	defaultWikiInjectTopK          = 1
	defaultWikiInjectMaxBodyTokens = 800

	wikiInjectTopKMin          = 1
	wikiInjectTopKMax          = 10
	wikiInjectMaxBodyTokensMin = 100
	wikiInjectMaxBodyTokensMax = 4000
)

// wikiInjectEnabled returns true when the operator opted in. Mirrors the
// staged-rollout pattern used by wikiAutoUpdateEnabled / atomicFactsEnabled.
func wikiInjectEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(wikiSearchInjectEnv)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// wikiInjectTopK reads MEMDB_WIKI_INJECT_TOP_K, clamped to [1,10]. Default 1
// because the chat prompt already carries retrieved memories; the wiki block
// is a synthesis hint, not a replacement.
func wikiInjectTopK() int {
	raw := strings.TrimSpace(os.Getenv(wikiInjectTopKEnv))
	if raw == "" {
		return defaultWikiInjectTopK
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultWikiInjectTopK
	}
	if n < wikiInjectTopKMin {
		return wikiInjectTopKMin
	}
	if n > wikiInjectTopKMax {
		return wikiInjectTopKMax
	}
	return n
}

// wikiInjectMaxBodyTokens reads MEMDB_WIKI_INJECT_MAX_BODY_TOKENS, clamped
// to [100,4000]. 800 ≈ 3200 chars per page — generous for one summary, still
// safely inside the 32k-token chat budget when combined with memories.
func wikiInjectMaxBodyTokens() int {
	raw := strings.TrimSpace(os.Getenv(wikiInjectMaxBodyTokensEnv))
	if raw == "" {
		return defaultWikiInjectMaxBodyTokens
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultWikiInjectMaxBodyTokens
	}
	if n < wikiInjectMaxBodyTokensMin {
		return wikiInjectMaxBodyTokensMin
	}
	if n > wikiInjectMaxBodyTokensMax {
		return wikiInjectMaxBodyTokensMax
	}
	return n
}

// dbWikiPage is the slimmed-down view fetchWikiSynthesis hands to the
// renderer. We strip Embedding / IDs / timestamps because the prompt only
// needs slug+title+body — keeps renderWikiInjectBlock unit-testable without
// pulling pgx types.
type dbWikiPage struct {
	slug  string
	title string
	body  string
}

// fetchWikiSynthesis embeds the user's query, searches wiki_pages by cosine
// similarity, and renders a "## Wiki Synthesis" block ready to prepend to the
// base prompt. Returns "" on any of: gate off, missing handles, empty query,
// embed error, search error, no results — so the caller can unconditionally
// concatenate without a nil check.
//
// All failures degrade silently (warn-log only). The wiki layer is a
// best-effort augmentation; a flaky pgvector query must never break chat.
func (h *Handler) fetchWikiSynthesis(ctx context.Context, cubeID, query string) string {
	if !wikiInjectEnabled() {
		return ""
	}
	if h.postgres == nil || h.embedder == nil {
		return ""
	}
	if cubeID == "" || strings.TrimSpace(query) == "" {
		return ""
	}

	vecs, err := h.embedder.Embed(ctx, []string{query})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		if err != nil {
			h.logger.Warn("wiki inject: embed failed",
				slog.String("cube_id", cubeID),
				slog.Any("error", err))
		}
		return ""
	}

	pages, err := h.postgres.SearchWikiByCosine(ctx, cubeID, vecs[0], wikiInjectTopK())
	if err != nil {
		h.logger.Warn("wiki inject: search failed",
			slog.String("cube_id", cubeID),
			slog.Any("error", err))
		return ""
	}
	if len(pages) == 0 {
		return ""
	}

	view := make([]dbWikiPage, 0, len(pages))
	for _, p := range pages {
		view = append(view, dbWikiPage{slug: p.Slug, title: p.Title, body: p.Body})
	}
	return renderWikiInjectBlock(view, wikiInjectMaxBodyTokens())
}

// renderWikiInjectBlock formats the cosine-matched pages into a single
// markdown section. Layout:
//
//	## Wiki Synthesis
//
//	> Pre-compiled summaries...
//
//	### <title>
//	<body>
//	---
//	### <title>
//	<body>
//
// Each body is independently truncated to maxBodyTokens to bound prompt
// growth — the chat budget gate (buildSystemPromptWithBudget) gives the
// final cut, this is just a per-page cap.
func renderWikiInjectBlock(pages []dbWikiPage, maxBodyTokens int) string {
	if len(pages) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Wiki Synthesis\n\n")
	sb.WriteString("> Pre-compiled summaries from prior consolidations. ")
	sb.WriteString("Use as background — defer to the per-turn memory block ")
	sb.WriteString("for facts that contradict.\n\n")
	for i, p := range pages {
		title := strings.TrimSpace(p.title)
		if title == "" {
			title = p.slug
		}
		fmt.Fprintf(&sb, "### %s\n", title)
		body := truncateMarkdown(p.body, maxBodyTokens)
		sb.WriteString(strings.TrimSpace(body))
		sb.WriteString("\n")
		if i < len(pages)-1 {
			sb.WriteString("\n---\n\n")
		}
	}
	return sb.String()
}

// truncateMarkdown caps body at approxTokens(body) ≤ maxTokens, cutting at
// the nearest paragraph break, then line break, then sentence terminator.
// Falls back to a hard char cut when no boundary exists in the budget.
//
// Token budget uses the chars/4 heuristic shared with approxTokens — exact
// tokenization is overkill here because the chat budget gate re-checks.
func truncateMarkdown(body string, maxTokens int) string {
	if maxTokens <= 0 {
		return body
	}
	maxChars := maxTokens * 4
	if len(body) <= maxChars {
		return body
	}
	cut := body[:maxChars]
	if idx := strings.LastIndex(cut, "\n\n"); idx > maxChars/2 {
		return body[:idx] + "\n\n…"
	}
	if idx := strings.LastIndex(cut, "\n"); idx > maxChars/2 {
		return body[:idx] + "\n…"
	}
	if idx := strings.LastIndexAny(cut, ".!?"); idx > maxChars/2 {
		return body[:idx+1] + " …"
	}
	return cut + "…"
}

