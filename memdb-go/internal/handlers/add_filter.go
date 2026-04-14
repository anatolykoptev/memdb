package handlers

// add_filter.go — post-extraction hallucination filter.
// Validates extracted facts against conversation, removing hallucinated memories.
// Port of Python SIMPLE_STRUCT_HALLUCINATION_FILTER_PROMPT.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const hallucinationFilterPrompt = `You are a strict memory validator.
Your task is to identify and delete hallucinated memories that are not explicitly stated by the user in the provided messages.

Rules:
1. **Explicit Denial & Inconsistency**: If a memory claims something that the user explicitly denied or is clearly inconsistent with the user's statements, mark it for deletion.
2. **Timestamp Exception**: Memories may include timestamps derived from conversation metadata. If the date in the memory is likely the conversation time, do NOT treat it as a hallucination.

Example:
Messages:
[user]: I'm planning a trip to Japan next month for about a week.
[assistant]: That sounds great! Are you planning to visit Tokyo Disneyland?
[user]: No, I won't be going to Tokyo this time. I plan to stay in Kyoto and Osaka to avoid crowds.

Memories:
{
  "0": "User plans to travel to Japan for a week next month.",
  "1": "User intends to visit Tokyo Disneyland.",
  "2": "User plans to stay in Kyoto and Osaka."
}

Output:
{
  "0": { "keep": true, "reason": "Explicitly stated by user." },
  "1": { "keep": false, "reason": "User explicitly denied visiting Tokyo." },
  "2": { "keep": true, "reason": "Explicitly stated by user." }
}

Inputs:
Messages:
%s

Memories:
%s

Output Format:
- Return a JSON object with string keys ("0", "1", "2", ...) matching the input memory indices.
- Each value must be: { "keep": boolean, "reason": string }
- "keep": true only if the memory is a direct reflection of the user's explicit words.
- "reason": brief, factual, and cites missing or unsupported content.

Important: Output **only** the JSON. No extra text, explanations, markdown, or fields.`

const hallucinationFilterMaxTokens = 1000

// filterVerdict is the per-fact decision from the hallucination filter LLM call.
type filterVerdict struct {
	Keep   bool   `json:"keep"`
	Reason string `json:"reason"`
}

// filterHallucinatedFacts validates extracted facts against the conversation,
// removing any that the user never explicitly stated. Facts are kept by default
// if the LLM call fails or a specific verdict cannot be parsed.
//
// Fast path: if the extractor already flagged any facts with Hallucinated=true,
// use those flags directly without a second LLM round-trip.
func (h *Handler) filterHallucinatedFacts(ctx context.Context, conversation string, facts []llm.ExtractedFact) []llm.ExtractedFact {
	// Skip LLM call for trivial sets: 0-2 facts rarely contain hallucinations
	// worth a full round-trip, and the extractor's own confidence filter already
	// handles the most common cases.
	if len(facts) <= 2 || h.llmChat == nil {
		return facts
	}

	// Fast path: extractor already populated Hallucinated field — use it directly.
	anyFlagged := false
	for _, f := range facts {
		if f.Hallucinated {
			anyFlagged = true
			break
		}
	}
	if anyFlagged {
		var kept []llm.ExtractedFact
		for _, f := range facts {
			if !f.Hallucinated {
				kept = append(kept, f)
			} else {
				h.logger.Debug("hallucination filter: removed fact (extractor flag)",
					slog.String("memory", f.Memory))
			}
		}
		return kept
	}

	// Slow path: extractor did not flag anything — run dedicated LLM filter.
	// Build indexed memories map: {"0": "fact text", "1": "fact text", ...}
	memories := make(map[string]string, len(facts))
	for i, f := range facts {
		memories[fmt.Sprintf("%d", i)] = f.Memory
	}
	memoriesJSON, err := json.Marshal(memories)
	if err != nil {
		h.logger.Warn("hallucination filter: marshal memories failed", slog.Any("error", err))
		return facts
	}

	prompt := fmt.Sprintf(hallucinationFilterPrompt, conversation, string(memoriesJSON))
	msgs := []map[string]string{
		{"role": "user", "content": prompt},
	}

	raw, err := h.llmChat.Chat(ctx, msgs, hallucinationFilterMaxTokens)
	if err != nil {
		h.logger.Warn("hallucination filter: llm call failed", slog.Any("error", err))
		return facts
	}

	// Strip markdown fences if present.
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var verdicts map[string]filterVerdict
	if err := json.Unmarshal([]byte(raw), &verdicts); err != nil {
		h.logger.Warn("hallucination filter: parse response failed",
			slog.Any("error", err), slog.String("raw", raw[:min(len(raw), 200)]))
		return facts
	}

	// Keep facts where verdict is missing or keep != false.
	var kept []llm.ExtractedFact
	filtered := 0
	for i, f := range facts {
		v, ok := verdicts[fmt.Sprintf("%d", i)]
		if !ok || v.Keep {
			kept = append(kept, f)
		} else {
			filtered++
			h.logger.Debug("hallucination filter: removed fact",
				slog.Int("index", i), slog.String("reason", v.Reason))
		}
	}

	if filtered > 0 {
		h.logger.Info("hallucination filter: removed facts",
			slog.Int("filtered", filtered), slog.Int("kept", len(kept)))
	}
	return kept
}
