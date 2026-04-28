// Package handlers — M11 F12 linked_memory_ids resolver.
//
// After the F8 atomic-fact persist step writes a memory with whatever
// linked_memory_ids the LLM emitted at extract-time (a thin set sourced from
// the ~10 dedup candidates), F12 enriches that set:
//
//  1. Pull top-N (default 20) cosine-similar memories for the new fact —
//     the wider candidate window the extract-time pass never saw.
//  2. Ask the LLM which of those candidates are causally / temporally
//     linked to the fact. Strict structured output, single round-trip.
//  3. Merge with the extract-time linked_memory_ids (dedup, cap), persist
//     via UpdateMemoryLinkedIDs (jsonb_set on properties->'linked_memory_ids').
//
// The persisted set powers the search-side stageLinkedExpand 1-hop GIN
// expansion: queries that hit a fact also surface its causally-linked
// neighbours via the GIN index migration 0022 already shipped.
//
// Env-gate: MEMDB_F12_LINKED — default ON (resolverEnabled = true) per the
// M11 plan. Set MEMDB_F12_LINKED=false to disable.
//
// Latency budget: this runs in the background fan-out so its cost lands on
// add-side wallclock only; the spec budget is +50ms p95 for the search-side
// stage (D2 pre-existing). Resolver itself is single LLM call per fact,
// expected ~1-2s with cached prompt — fully off the request path.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// linkedResolverEnvVar gates the F12 resolver. Default ON.
const linkedResolverEnvVar = "MEMDB_F12_LINKED"

// linkedResolverDefaults — knobs sized to keep the call cheap without losing
// the long-tail relations the extract-time pass missed.
const (
	linkedResolverTopN     = 20            // candidate window (cosine top-N)
	linkedResolverMaxLinks = 8             // hard cap per fact (avoid jsonb bloat)
	linkedResolverTimeout  = 30 * time.Second
	linkedResolverPromptID = "linked_resolver"
	linkedResolverMaxTok   = 512
)

// linkedResolverEnabled returns true when MEMDB_F12_LINKED is unset, empty,
// or set to a truthy value. Default ON per spec.
func linkedResolverEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(linkedResolverEnvVar)))
	switch v {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// linkedResolverResponse is the LLM's structured output: a flat list of
// candidate UUIDs the model believes are causally / temporally linked.
//
// We deliberately do NOT ask the LLM for a "reason" or "relation type" —
// the GIN index migration 0022 stores a flat string array, and adding a
// relation kind would force a schema change for marginal F1 gain. F11
// (edge invalidation judge) already covers richer per-edge metadata for
// entity edges.
type linkedResolverResponse struct {
	LinkedIDs []string `json:"linked_ids"`
}

// LinkedIDsResolver wraps the chat client used for the F12 LLM call. The
// resolver re-uses the LLMExtractor's client so credentials / fallback
// model lists stay in lockstep with F8.
type LinkedIDsResolver struct {
	client *llm.Client
	logger *slog.Logger
}

// NewLinkedIDsResolver builds a resolver bound to the supplied chat client.
// Pass the same client used by the AtomicExtractor.
func NewLinkedIDsResolver(c *llm.Client, logger *slog.Logger) *LinkedIDsResolver {
	return &LinkedIDsResolver{client: c, logger: logger}
}

// Resolve asks the LLM which of `candidates` are causally or temporally
// linked to `fact.Text`. Returns the filtered, validated UUID list (each
// guaranteed to (a) parse as UUID and (b) appear in candidates). Capped at
// linkedResolverMaxLinks. nil candidates / nil client / empty fact text =>
// (nil, nil) — caller treats that as a no-op.
//
// The resolver is intentionally side-effect-free: it does NOT touch the
// database. The caller (resolveAndPersistLinkedIDs) merges with the
// extract-time IDs and writes via UpdateMemoryLinkedIDs.
func (r *LinkedIDsResolver) Resolve(
	ctx context.Context,
	fact llm.AtomicFact,
	candidates []llm.Candidate,
) ([]string, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("LinkedIDsResolver: nil client")
	}
	if fact.Text == "" || len(candidates) == 0 {
		return nil, nil
	}

	user := buildLinkedResolverPrompt(fact, candidates)
	msgs := []llm.Message{
		{Role: "system", Content: linkedResolverSystem},
		{Role: "user", Content: user},
	}

	var resp linkedResolverResponse
	err := llm.ChatStructured(ctx, r.client, linkedResolverPromptID, msgs, &resp,
		llm.WithMaxTokens(linkedResolverMaxTok),
		llm.WithTimeout(linkedResolverTimeout),
		llm.WithMaxRetries(1),
	)
	if err != nil {
		return nil, fmt.Errorf("linked resolver: %w", err)
	}

	// Filter: only candidates the LLM was actually offered, valid UUID,
	// dedup, capped. Anything else is a hallucination.
	allowed := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		allowed[c.ID] = struct{}{}
	}
	out := make([]string, 0, len(resp.LinkedIDs))
	seen := make(map[string]struct{}, len(resp.LinkedIDs))
	for _, id := range resp.LinkedIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := allowed[id]; !ok {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		if _, perr := uuid.Parse(id); perr != nil {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= linkedResolverMaxLinks {
			break
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// linkedResolverSystem is the F12 system prompt. Kept inline (unlike the
// 33 KB ADDITIVE prompt) because it's small and version-tracking is
// adequate via git log on this file.
const linkedResolverSystem = `You are a knowledge-graph linker.

You are given ONE new factual statement and a numbered list of EXISTING memories. Decide which existing memories are causally or temporally linked to the new fact.

A memory is "linked" when:
  - it shares the same entity AND a continuation/contradiction relationship with the new fact
  - the new fact updates a preference, status, or attribute the existing memory established
  - the new fact and the existing memory describe a cause-effect or before-after sequence about the same entity or event

Do NOT link merely because two memories mention the same person, topic, or vague theme. The relationship must be specific (same event, same entity attribute, same plan being updated).

Return strict JSON, no prose, no code fence:
{"linked_ids": ["<uuid-from-the-list>", "..."]}

Use ONLY UUIDs that appear in the candidate list. If none qualify, return {"linked_ids": []}.`

// buildLinkedResolverPrompt assembles the user message: the new fact + a
// numbered list of (uuid, memory text) candidates. Mirrors the
// buildAtomicUserMessage shape so prompt-version diffs are easy to read.
func buildLinkedResolverPrompt(fact llm.AtomicFact, candidates []llm.Candidate) string {
	var sb strings.Builder
	sb.WriteString("## New Fact\n")
	sb.WriteString(fact.Text)
	if fact.AttributedTo != "" {
		fmt.Fprintf(&sb, "\nAttributed to: %s", fact.AttributedTo)
	}
	if len(fact.EventDates) > 0 {
		fmt.Fprintf(&sb, "\nEvent dates: %s", strings.Join(fact.EventDates, ", "))
	}
	sb.WriteString("\n\n## Candidate Memories\n")
	for _, c := range candidates {
		fmt.Fprintf(&sb, "- id=%s text=%q\n", c.ID, c.Memory)
	}
	sb.WriteString("\nReturn JSON only.\n")
	return sb.String()
}

// mergeLinkedIDs merges the extract-time set with the resolver-emitted set,
// dedups, preserves order (extract-time first), and caps at
// linkedResolverMaxLinks. Returns nil when the merged set is empty so the
// caller can omit the property from the JSONB blob entirely.
func mergeLinkedIDs(existing, resolved []string) []string {
	if len(existing) == 0 && len(resolved) == 0 {
		return nil
	}
	out := make([]string, 0, len(existing)+len(resolved))
	seen := make(map[string]struct{}, len(existing)+len(resolved))
	for _, id := range existing {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		if _, perr := uuid.Parse(id); perr != nil {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= linkedResolverMaxLinks {
			return out
		}
	}
	for _, id := range resolved {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		if _, perr := uuid.Parse(id); perr != nil {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= linkedResolverMaxLinks {
			return out
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
