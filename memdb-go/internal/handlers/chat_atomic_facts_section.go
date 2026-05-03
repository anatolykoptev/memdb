package handlers

// chat_atomic_facts_section.go — Path X (Memobase parity, 2026-05-01).
//
// Surfaces top-K atomic facts (kind='atomic_fact', cosine-ranked against the
// user query) as a "## Key Facts" block injected ahead of the existing
// "## User Profile" section in the chat system prompt.
//
// Forensic Karpathy r3 (2026-05-01) found atomic facts already extracted in
// the DB are high quality (e.g. "Caroline attended an LGBTQ support group on
// May 7, 2023…"), but they only surface through the generic VectorSearch path
// where they compete on cosine against verbose paragraph_legacy rows and lose
// — shorter text → lower raw cosine on long queries → drops out of top-K.
//
// Memobase ships these as a guaranteed inline block so the LLM ALWAYS sees
// them; this file mirrors that pattern. Parity intentional.
//
// Render shape (Memobase-style bullets, attributed + first event date):
//
//	## Key Facts
//	The following are extracted atomic facts about the conversation participants. Use these as primary evidence.
//	- Caroline (2023-05-07) Caroline attended an LGBTQ support group ...
//	- Melanie Melanie loves jazz ...
//
// Empty result → "" so the caller can skip the section entirely (no header
// emitted, prompt budget preserved).

import (
	"context"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	atomicFactsSectionHeader = "## Key Facts"
	// atomicFactsSectionGuard mirrors profileGuardSentence's intent:
	// data-vs-instruction boundary marker. The atomic-fact memos are user
	// utterances paraphrased by the extractor, so the same prompt-injection
	// risk applies (audit C2 in chat_prompt_profile.go).
	atomicFactsSectionGuard = "The following are extracted atomic facts about the conversation participants. Use these as primary evidence."
	// atomicFactsMaxRows — hardcoded for Path X start. Memobase ships ~10 in
	// their reference impl; 12 leaves a small headroom without bloating the
	// system prompt. Make env-tunable in a follow-up if dashboards show the
	// section consistently truncating useful tail rows.
	atomicFactsMaxRows = 12
)

// chatAtomicFactsSection fetches top-K atomic facts by cosine similarity to
// queryVec across the supplied cubes and renders them as the "## Key Facts"
// prompt block. Returns "" on any of:
//   - cubeIDs / queryVec empty (nothing to scope or score)
//   - DB error (logged, swallowed — best-effort, must never block chat)
//   - zero atomic facts in the cube (cold cube)
//
// The empty-string contract lets the caller concatenate with profileSection
// without emitting a stray header when the cube has no atomic facts yet.
func (h *Handler) chatAtomicFactsSection(
	ctx context.Context, cubeIDs []string, queryVec []float32,
) string {
	if h.postgres == nil || len(cubeIDs) == 0 || len(queryVec) == 0 {
		return ""
	}
	rows, err := h.postgres.GetTopAtomicFactsByCosine(ctx, cubeIDs, queryVec, atomicFactsMaxRows)
	if err != nil {
		h.logger.Warn("chat atomic facts fetch failed",
			slog.Int("cubes", len(cubeIDs)),
			slog.Any("error", err))
		return ""
	}
	if len(rows) == 0 {
		return ""
	}
	return formatAtomicFactsSection(rows)
}

// formatAtomicFactsSection renders the bullet block. Split out from the
// fetch wrapper so unit tests can exercise rendering without a live DB.
func formatAtomicFactsSection(rows []db.AtomicFactRow) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(atomicFactsSectionHeader)
	b.WriteByte('\n')
	b.WriteString(atomicFactsSectionGuard)
	b.WriteByte('\n')
	for _, r := range rows {
		if strings.TrimSpace(r.Memory) == "" {
			continue
		}
		b.WriteString("- ")
		if r.AttributedTo != "" {
			b.WriteString(escapeProfileMemo(r.AttributedTo))
			b.WriteByte(' ')
		}
		if len(r.EventDates) > 0 && r.EventDates[0] != "" {
			b.WriteByte('(')
			b.WriteString(escapeProfileMemo(r.EventDates[0]))
			b.WriteString(") ")
		}
		b.WriteString(escapeProfileMemo(r.Memory))
		b.WriteByte('\n')
	}
	return b.String()
}

// allCubeIDsForChat returns the set of cube IDs the chat request reads from.
// Dual/multi-speaker requests (Speakers >= 2) use one cube per speaker —
// each speaker's atomic facts must be visible to the LLM, so we fan out.
// Single-speaker requests fall back to resolveCubeIDs (readable_cube_ids
// → mem_cube_id → user_id), matching the chat search path.
//
// Returns nil when the request carries no usable identifier; callers MUST
// treat nil as "skip the atomic-facts section" (the SQL probe does the same
// guard, but checking here saves a pool acquire).
func allCubeIDsForChat(req *nativeChatRequest) []string {
	if req == nil {
		return nil
	}
	if len(req.Speakers) >= 2 {
		out := make([]string, 0, len(req.Speakers))
		for _, sp := range req.Speakers {
			if sp != "" {
				out = append(out, sp)
			}
		}
		return out
	}
	return resolveCubeIDs(req.ReadableCubeIDs, req.MemCubeID, req.UserID)
}

// joinPromptSections concatenates the optional Key Facts and User Profile
// sections passed to buildSystemPromptWithBudget. Either side may be empty;
// when both are non-empty they are joined with a single newline so the
// downstream prompt template still gets a single contiguous block in the
// `profileSection` slot. We deliberately put facts FIRST: they are the
// higher-signal evidence (cosine-ranked to the query) and Memobase places
// them before the persistent profile in their reference impl.
func joinPromptSections(facts, profile string) string {
	switch {
	case facts == "" && profile == "":
		return ""
	case facts == "":
		return profile
	case profile == "":
		return facts
	default:
		return facts + "\n" + profile
	}
}

// chatEmbedQuery returns the dense embedding for the chat query. Returns nil
// on missing embedder / embed error / empty result (caller skips the
// atomic-facts section). One-shot helper so the chat handler doesn't grow a
// second per-request embed cache; the embed cost is ~5–15ms vs the LLM call's
// hundreds of ms, so re-embedding for the atomic-facts section is acceptable
// even when the search path also embeds the query.
func (h *Handler) chatEmbedQuery(ctx context.Context, query string) []float32 {
	if h.embedder == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	vecs, err := h.embedder.Embed(ctx, []string{query})
	if err != nil {
		h.logger.Warn("chat atomic facts: query embed failed",
			slog.Any("error", err))
		return nil
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil
	}
	return vecs[0]
}
