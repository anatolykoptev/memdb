package handlers

// chat_prompt.go — prompt templates and memory formatting for chat endpoints.
// M12.2 conditional refusal logic lives in chat_prompt_softening.go.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// chatNowOverrideEnvVar is the env-var consulted by chatPromptNow before
// falling back to time.Now(). M12.1: lets harnesses (LoCoMo, replay benches)
// pin the "Current Time" baseline against a historic conversation date so
// the model resolves "last week" / "yesterday" against the right anchor.
//
// Format: any string the operator wants the prompt to display verbatim.
// Empty / unset preserves the legacy time.Now() behaviour byte-for-byte
// (zero regression for production clients).
const chatNowOverrideEnvVar = "MEMDB_CHAT_NOW_OVERRIDE"

// chatPromptNow returns the string to inject into the "Current Time" header
// of cloud / factual chat prompts. Honours MEMDB_CHAT_NOW_OVERRIDE when set
// (whitespace-trimmed, non-empty), else falls back to wall-clock formatted
// to match the historic %Y-%m-%d %H:%M (%A) layout.
//
// Reading the env on each call (vs. at process start) is intentional: keeps
// integration tests cheap (no t.Setenv + restart) and the per-call cost is a
// single map lookup — chat is already an LLM round-trip path.
func chatPromptNow() string {
	if v := strings.TrimSpace(os.Getenv(chatNowOverrideEnvVar)); v != "" {
		return v
	}
	return time.Now().Format("2006-01-02 15:04 (Monday)")
}

// buildSystemPrompt constructs the chat system prompt with memory context.
// Routing precedence:
//  1. basePrompt != "" → use it as-is (custom system_prompt always wins, backward compat).
//  2. answerStyle == "factual" → factualQAPrompt<HighConfidence|LowConfidence><EN|ZH>
//     chosen by (decideFactualPrompt(memories), detectLang(query)).
//  3. otherwise → cloudChatPrompt<EN|ZH> (existing default).
//
// answerStyle values are validated upstream by validateChatRequest; this function
// treats any unknown value as the default branch (defensive — should never hit).
func buildSystemPrompt(query string, memories []map[string]any, prefString, basePrompt, answerStyle string) string {
	prompt, _ := buildSystemPromptWithDecision(context.TODO(), query, memories, prefString, basePrompt, answerStyle, "")
	return prompt
}

// buildSystemPromptWithProfile preserves the M10 Stream 3 entry point used by
// existing chat handlers. Returns only the rendered prompt — the decision is
// dropped. New call sites that need metrics/headers should use
// buildSystemPromptWithDecision.
func buildSystemPromptWithProfile(ctx context.Context, query string, memories []map[string]any, prefString, basePrompt, answerStyle, profileSection string) string {
	prompt, _ := buildSystemPromptWithDecision(ctx, query, memories, prefString, basePrompt, answerStyle, profileSection)
	return prompt
}

// buildSystemPromptWithDecision is the underlying prompt-builder. It returns
// both the rendered system prompt AND the factualPromptDecision so chat
// handlers can emit X-Memdb-Refusal-Reason and metrics without re-deriving
// the threshold logic.
//
// For non-factual branches (custom basePrompt, conversational default) the
// returned decision has Variant=factualVariantNone and Reason=refusalReasonNone.
//
// profileSection — pre-rendered output of formatProfileSection (empty string
// means: do not prepend, e.g. when the env gate is disabled or the caller
// chose not to fetch profiles). When non-empty, the section is prepended with
// a blank line separator, BEFORE the existing memory section. The existing
// memory templates are not modified — this is strictly additive.
//
// The profile section is also prepended to custom basePrompt branches so the
// two-section ordering contract holds regardless of which template wins.
func buildSystemPromptWithDecision(_ context.Context, query string, memories []map[string]any, prefString, basePrompt, answerStyle, profileSection string) (string, factualPromptDecision) {
	return buildSystemPromptWithBudget(query, memories, prefString, basePrompt, answerStyle, profileSection, 0)
}

// buildSystemPromptWithBudget mirrors buildSystemPromptWithDecision but
// honours a per-request token cap on the memories block. Callers that
// expose MaxContextTokens to the API surface (chat.NativeChatComplete /
// NativeChatStream) route through here; legacy callers stay on the
// 7-arg wrapper above with maxContextTokens=0 (no cap, byte-identical
// behaviour).
func buildSystemPromptWithBudget(query string, memories []map[string]any, prefString, basePrompt, answerStyle, profileSection string, maxContextTokens int) (string, factualPromptDecision) {
	memCtx := formatMemories(memories, prefString, maxContextTokens)

	decision := factualPromptDecision{Variant: factualVariantNone, Reason: refusalReasonNone}

	// M12.4: when the caller supplied a custom system_prompt AND requested the
	// factual answer style (LoCoMo dual-speaker harness, any harness wrapping
	// per-speaker memory blocks into a custom prompt), classify the memory pool
	// up-front so we can inject the variant-conditional anti-refusal rules.
	// Pre-fix: this branch fell through with decision.Variant=none and the
	// rendered prompt was basePrompt + memCtx — the M12.2 commit/no-refuse
	// rules were silently dropped, producing 48% chat_refused_with_evidence on
	// full-corpus eval. The injection adds a "## Answer Rules" block AFTER the
	// caller's prompt and BEFORE the memory section.
	customFactual := basePrompt != "" && answerStyle == answerStyleFactual
	if customFactual {
		decision = decideFactualPrompt(memories)
	}

	var rendered string
	switch {
	case basePrompt == "":
		lang := detectLang(query)
		var tpl string
		if answerStyle == answerStyleFactual {
			decision = decideFactualPrompt(memories)
			tpl = pickFactualTemplate(decision.Variant, lang)
		} else if lang == "zh" {
			tpl = cloudChatPromptZH
		} else {
			tpl = cloudChatPromptEN
		}
		now := chatPromptNow()
		rendered = fmt.Sprintf(tpl, now, memCtx)
	case strings.Contains(basePrompt, "{memories}"):
		// Caller controls placement of memCtx. Append rules block AFTER the
		// substituted prompt so the rules apply on top of caller-customised
		// context layout. Rules-then-memory ordering is not guaranteed here
		// (caller decides), but the rules are still in scope of the same
		// system message.
		rendered = strings.Replace(basePrompt, "{memories}", memCtx, 1)
		if customFactual {
			rendered = rendered + "\n\n" + buildFactualRulesBlock(decision.Variant)
		}
	case len(memories) > 0:
		rendered = basePrompt
		if customFactual {
			rendered += "\n\n" + buildFactualRulesBlock(decision.Variant)
		}
		rendered += "\n\n## Fact Memories:\n" + memCtx
	default:
		rendered = basePrompt
		if customFactual {
			// Zero-memory custom-factual: still emit the rules block so the
			// model sees the "no answer" refusal contract from the low-conf
			// body (decision.Variant=zero falls into the low-EN path inside
			// buildFactualRulesBlock).
			rendered += "\n\n" + buildFactualRulesBlock(decision.Variant)
		}
	}

	if profileSection != "" {
		rendered = profileSection + "\n" + rendered
	}
	return rendered, decision
}

// Memory list shaping (formatMemories, filterMemoriesByThreshold and the
// relativity/memType/sortByRelativity/safeSlice helpers) lives in
// chat_memories.go — same package, separate file so prompt assembly and
// memory shaping evolve independently.
