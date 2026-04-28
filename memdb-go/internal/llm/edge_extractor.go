package llm

// edge_extractor.go — M11 F11 bi-temporal edge invalidation judge.
//
// Given a new fact (subject + predicate + object) and the set of existing
// facts on the same subject, ask the LLM whether the new fact contradicts or
// supersedes any of the prior ones. The judge returns IDs of edges to mark
// `invalidated` along with a confidence in [0,1].
//
// Confidence threshold: callers gate writes on Confidence >= 0.7 — the actual
// `UPDATE … SET invalid_at = NOW() WHERE id = ANY(...)` is the caller's
// responsibility; this package only owns the LLM call.
//
// Reference: arxiv 2501.13956 (Graphiti / Zep) — every edge is a triple
// (subject, predicate, object) with valid_at + invalidated_at; an LLM judge
// flips invalidated_at when a newer triple supersedes an older one.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// edgeJudgeMaxFacts caps how many existing facts we feed the judge per call.
	// Bigger windows hurt latency + cost without a meaningful recall bump on
	// LoCoMo (most contradictions live in the 5 most-recent facts on a subject).
	edgeJudgeMaxFacts = 8

	// edgeJudgeMaxTokens — invalidate-list + scalar confidence is small.
	edgeJudgeMaxTokens = 200

	// edgeJudgeTimeout caps wall time per call. The judge is best-effort —
	// background work — and must not block scheduler goroutines.
	edgeJudgeTimeout = 20 * time.Second

	// EdgeInvalidationConfidenceThreshold is the floor for actually writing
	// invalid_at. Below this the LLM is too uncertain — log + noop. Exposed
	// so callers and tests share the same constant.
	EdgeInvalidationConfidenceThreshold = 0.7

	// edgeJudgePromptID tags this prompt for ChatStructured per-prompt metrics.
	edgeJudgePromptID = "f11_edge_invalidate"
)

// FactRef is a compact reference to one existing edge that the judge may flag
// for invalidation. ID is opaque to the judge — caller maps it back to the
// underlying row (memory_edges PK, entity_edges PK, etc.).
type FactRef struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// InvalidationDecision is the structured response shape. The judge returns
// the subset of FactRef.IDs that the new fact supersedes, plus a single
// confidence over the whole decision (not per-ID — the LLM is bad at that
// and Graphiti's reference impl uses a global score too).
type InvalidationDecision struct {
	Invalidate []string `json:"invalidate"`
	Confidence float64  `json:"confidence"`
	// Reason is optional free-form justification for logs; not load-bearing.
	Reason string `json:"reason,omitempty"`
}

// edgeJudgeSystemPrompt instructs the LLM to act as a fact-supersession judge.
// Kept short and factual per the M7 short-prompt finding (factual prompts
// improve F1 + p95). The schema is enforced by ChatStructured + retry.
const edgeJudgeSystemPrompt = `You are a fact supersession judge for a memory graph.

Given a NEW fact about a subject and a list of EXISTING facts about the same subject, decide which existing facts the new fact contradicts or supersedes (so they should be invalidated).

Return strict JSON: {"invalidate": ["<id1>", "<id2>"], "confidence": <0.0-1.0>, "reason": "<short>"}.

Rules:
- Only mark a fact for invalidation if the NEW fact directly contradicts it or replaces it (e.g. "lives in Berlin" supersedes "lives in Paris" for the same person; "works at Acme" does NOT supersede "lives in Berlin").
- Two facts that simply add detail without conflict are NOT supersessions — return "invalidate": [].
- "confidence" reflects how sure you are about the entire decision, not per-ID.
- If unsure, return low confidence (< 0.5). Never invent IDs.`

// DecideInvalidation asks the LLM judge whether newFact (about subject) makes
// any of existingFacts obsolete. Returns the decision and any error from the
// LLM round-trip / parse.
//
// Caller is responsible for:
//   - Filtering existingFacts to the same subject (we trust the input).
//   - Capping len(existingFacts) at edgeJudgeMaxFacts (we re-cap defensively).
//   - Gating actual UPDATE on Confidence >= EdgeInvalidationConfidenceThreshold.
//
// On empty input (no candidates), returns a zero-value decision and nil error
// without calling the LLM — saves a round-trip on subjects that have never
// been mentioned before, which is the common case for early /add cycles.
func DecideInvalidation(
	ctx context.Context,
	cli *Client,
	newFact, subject string,
	existingFacts []FactRef,
) (InvalidationDecision, error) {
	if cli == nil {
		return InvalidationDecision{}, errors.New("llm.DecideInvalidation: nil client")
	}
	if strings.TrimSpace(newFact) == "" || strings.TrimSpace(subject) == "" {
		return InvalidationDecision{}, nil
	}
	if len(existingFacts) == 0 {
		return InvalidationDecision{}, nil
	}
	if len(existingFacts) > edgeJudgeMaxFacts {
		existingFacts = existingFacts[:edgeJudgeMaxFacts]
	}

	user := buildEdgeJudgeUserPrompt(newFact, subject, existingFacts)

	callCtx, cancel := context.WithTimeout(ctx, edgeJudgeTimeout)
	defer cancel()

	var out InvalidationDecision
	err := ChatStructured(callCtx, cli, edgeJudgePromptID, []Message{
		{Role: "system", Content: edgeJudgeSystemPrompt},
		{Role: "user", Content: user},
	}, &out, WithMaxTokens(edgeJudgeMaxTokens))
	if err != nil {
		return InvalidationDecision{}, fmt.Errorf("llm.DecideInvalidation: %w", err)
	}

	// Normalise the result: clamp confidence, drop any IDs the LLM
	// hallucinated that aren't in the input set.
	if out.Confidence < 0 {
		out.Confidence = 0
	} else if out.Confidence > 1 {
		out.Confidence = 1
	}
	out.Invalidate = filterKnownIDs(out.Invalidate, existingFacts)
	return out, nil
}

// buildEdgeJudgeUserPrompt formats the user-side prompt for the judge.
// Kept simple: subject header, new fact, then a numbered/IDed list of existing
// facts. The judge replies with strict JSON per edgeJudgeSystemPrompt.
func buildEdgeJudgeUserPrompt(newFact, subject string, existingFacts []FactRef) string {
	var b strings.Builder
	b.WriteString("Subject: ")
	b.WriteString(subject)
	b.WriteString("\n\nNEW fact: ")
	b.WriteString(newFact)
	b.WriteString("\n\nEXISTING facts:\n")
	for _, f := range existingFacts {
		b.WriteString("- id=")
		b.WriteString(f.ID)
		b.WriteString(" text=")
		b.WriteString(f.Text)
		b.WriteByte('\n')
	}
	b.WriteString("\nWhich EXISTING facts (by id) does the NEW fact contradict or supersede?")
	return b.String()
}

// filterKnownIDs drops any IDs the LLM returned that don't correspond to an
// input FactRef. Defensive guard against hallucinated IDs flowing into the
// SQL UPDATE.
func filterKnownIDs(ids []string, existing []FactRef) []string {
	if len(ids) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(existing))
	for _, f := range existing {
		known[f.ID] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := known[id]; ok {
			out = append(out, id)
		}
	}
	return out
}
