// Package search — reflection_agent.go: F2 reflection-loop deep-search agent.
//
// After the staged retrieval / rerank pipeline produces a candidate set, the
// reflection agent asks an LLM whether the top candidates are sufficient to
// answer the query. The structured decision drives one optional follow-up
// fetch:
//
//   - decision == "sufficient"     → no extra work; return as-is.
//   - decision == "needs_raw"      → caller fetches top-K raw memories
//                                    (vector recall) WITHOUT rerank trim.
//   - decision == "missing_info"   → caller embeds each `missing_aspects`
//                                    phrase and merges the results.
//
// The agent itself is pure — it makes ONE LLM call and returns the parsed
// Decision. Loop control (max_iter=2) and any DB / embed work live in the
// pipeline stage so the agent stays unit-testable with a mock LLM.
//
// Env gating:
//   - MEMDB_F2_REFLECTION = "true" / "1"  → enable the stage (default OFF).
//   - MEMDB_REFLECTION_ON_COMPLEX_ONLY = "true" (default) → only run on
//     queries that match the cat-2 / cat-3 complexity heuristic.
//
// Latency budget: +200 ms p95 when enabled (one extra LLM call per query
// that passes the gate). Cap enforced via WithTimeout below.
package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// Reflection decision labels — also the JSON values the LLM is asked to emit.
const (
	ReflectionDecisionSufficient  = "sufficient"
	ReflectionDecisionNeedsRaw    = "needs_raw"
	ReflectionDecisionMissingInfo = "missing_info"
)

// reflectionPromptID tags the LLM call for ChatStructured per-prompt metrics.
const reflectionPromptID = "f2_reflection"

// reflectionMaxIter caps the reflection loop. 2 = first pass + at most one
// extra fetch, matching the M11 plan's "max_iter=2" requirement and the
// +200ms p95 latency budget (one LLM call ≈ 100-150ms via gemini-flash-lite).
const reflectionMaxIter = 2

// reflectionMaxAspects limits how many missing-aspect phrases we honour from
// the LLM response. Bounded to keep extra-embed fan-out predictable.
const reflectionMaxAspects = 3

// reflectionCallTimeout caps a single Reflect() LLM call. Kept tight because
// the whole feature is gated by a +200ms latency budget.
const reflectionCallTimeout = 4 * time.Second

// reflectionRespBodyLimit caps the LLM response body. The schema is small
// (decision + 1-3 short aspects + reason) — 8 KiB is plenty.
const reflectionRespBodyLimit = 8 * 1024

// reflectionMaxTokens limits LLM output. The schema is tiny; 256 is generous.
const reflectionMaxTokens = 256

// reflectionContextTopK is how many top items to include in the prompt as
// "memories retrieved so far". Mirrors iterativeMemContextTopK to keep
// prompts comparable in size and behaviour.
const reflectionContextTopK = 5

// Decision is the structured output of the reflection agent. JSON tags
// match the schema the LLM is asked to emit; extra fields (Reason) are
// optional and used only for logging / metrics.
type Decision struct {
	// Decision is one of ReflectionDecisionSufficient / NeedsRaw / MissingInfo.
	// Any other value is treated as "sufficient" (fail-closed: never trigger
	// extra DB / embed work on an unrecognised decision).
	Decision string `json:"decision"`
	// MissingAspects is a 0-3-element slice of short noun phrases the LLM
	// thinks would help close the answer gap. Only used when
	// Decision == MissingInfo. Empty when Decision == NeedsRaw or Sufficient.
	MissingAspects []string `json:"missing_aspects,omitempty"`
	// Reason is a one-sentence explanation. Optional; logged for triage.
	Reason string `json:"reason,omitempty"`
}

// IsValid reports whether the Decision label is one of the three documented
// outcomes. Used by the pipeline to decide which follow-up branch to take.
func (d Decision) IsValid() bool {
	switch d.Decision {
	case ReflectionDecisionSufficient, ReflectionDecisionNeedsRaw, ReflectionDecisionMissingInfo:
		return true
	default:
		return false
	}
}

// ReflectionAgent makes a single LLM "are these results enough?" judgement
// per Reflect() call. Stateless aside from the underlying llm.Client.
type ReflectionAgent struct {
	client *llm.Client
}

// NewReflectionAgent builds a ReflectionAgent that reuses the supplied
// chat Client. Pass the same client used by the rest of the search service
// so credentials and fallback model lists stay in sync. nil client means
// the agent is disabled — Reflect returns the disabled decision.
func NewReflectionAgent(c *llm.Client) *ReflectionAgent {
	return &ReflectionAgent{client: c}
}

// Reflect asks the LLM whether `results` are sufficient to answer `query`.
//
// On any error (nil client, transport, JSON parse, unknown decision label)
// the function returns a Decision with Decision == ReflectionDecisionSufficient
// alongside the error so callers can fail closed (skip extra fetch) AND log.
//
// The function emits memdb.reflection.duration_ms and updates the
// iterations_total counter with the appropriate outcome label.
func (a *ReflectionAgent) Reflect(ctx context.Context, query string, results []map[string]any) (Decision, error) {
	mx := reflectionMx()
	start := time.Now()
	defer func() {
		mx.DurationMS.Record(ctx, time.Since(start).Milliseconds())
	}()

	if a == nil || a.client == nil {
		return Decision{Decision: ReflectionDecisionSufficient}, errors.New("ReflectionAgent: nil client")
	}

	memCtx := buildReflectionContext(results, reflectionContextTopK)
	system, user := buildReflectionPrompt(query, memCtx)

	var dec Decision
	err := llm.ChatStructured(ctx, a.client, reflectionPromptID, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, &dec,
		llm.WithMaxTokens(reflectionMaxTokens),
		llm.WithTimeout(reflectionCallTimeout),
		llm.WithRespBodyLimit(reflectionRespBodyLimit),
		llm.WithMaxRetries(1),
	)
	if err != nil {
		return Decision{Decision: ReflectionDecisionSufficient},
			fmt.Errorf("reflection agent: %w", err)
	}

	dec.Decision = strings.TrimSpace(dec.Decision)
	if !dec.IsValid() {
		// Fail closed: any unrecognised label collapses to "sufficient" so we
		// don't trigger extra DB / embed work on garbage output.
		return Decision{Decision: ReflectionDecisionSufficient, Reason: dec.Reason},
			fmt.Errorf("reflection agent: unknown decision label %q", dec.Decision)
	}
	dec.MissingAspects = sanitizeAspects(dec.MissingAspects)
	return dec, nil
}

// buildReflectionPrompt composes the system + user messages for the
// reflection agent. Kept as plain strings (no embed) so the prompt is
// reviewable in code review without opening a separate file.
func buildReflectionPrompt(query, memCtx string) (string, string) {
	system := `You are a memory retrieval reflection agent.

You receive:
1. A user query.
2. A short list of memory fragments retrieved so far.

Decide whether the retrieved memories are sufficient to answer the query.

Output JSON with EXACTLY this schema (no extra fields, no markdown):

{
  "decision": "sufficient" | "needs_raw" | "missing_info",
  "missing_aspects": ["short noun phrase", ...],
  "reason": "one short sentence"
}

Rules:
- "sufficient" — the memories already contain enough to answer. missing_aspects MUST be [].
- "needs_raw" — the memories are reranked / trimmed and likely lost detail; we should fetch raw top-K. missing_aspects MUST be [].
- "missing_info" — there is a specific factual gap. Provide 1-3 short noun phrases describing what is missing.

Be conservative: prefer "sufficient" when in doubt. Never invent facts.`

	user := fmt.Sprintf("Query: %s\n\nMemories retrieved so far:\n%s\n\nJSON:", query, memCtx)
	return system, user
}

// buildReflectionContext renders top-N items as a numbered bullet list.
// Mirrors buildMemoryContext from iterative_retrieval.go but kept separate
// so future prompt tuning of the reflection agent doesn't ripple back into
// the iterative-retrieval prompt.
func buildReflectionContext(items []map[string]any, n int) string {
	if n > len(items) {
		n = len(items)
	}
	if n == 0 {
		return "(none)"
	}
	var sb strings.Builder
	for i, item := range items[:n] {
		mem, _ := item["memory"].(string)
		mem = strings.TrimSpace(mem)
		if mem == "" {
			continue
		}
		fmt.Fprintf(&sb, "%d. %s\n", i+1, mem)
	}
	if sb.Len() == 0 {
		return "(none)"
	}
	return sb.String()
}

// sanitizeAspects trims, drops empties, deduplicates and caps the missing
// aspects slice. Returns nil for an empty input so omitempty drops the JSON
// key downstream (we don't usually re-emit Decision, but tests expect it).
func sanitizeAspects(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
		if len(out) >= reflectionMaxAspects {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
