package handlers

// chat_dual_prompt.go — dual-speaker SYSTEM PROMPT assembly.
//
// Single responsibility: turn []chatDualSpeakerLeg into the textual
// system prompt fed to the chat LLM. No I/O, no DB, no metrics.
//
// Mirrors the harness equivalent in
// evaluation/locomo/query.py::_build_dual_speaker_system_prompt so the
// server-side path produces the same shape (header + Current time +
// per-speaker blocks + ts: prefixes) regardless of caller.

import (
	"fmt"
	"os"
	"strings"
)

// dualSpeakerChatPromptHeader is prepended to the dual-speaker system
// prompt. Echoes the harness header tone ("multi-speaker conversation",
// "treat every speaker's evidence equally") but stays template-neutral
// so buildSystemPromptWithDecision can append the M12.4 factual rules.
const dualSpeakerChatPromptHeader = "You are a factual QA assistant answering a question about a recorded multi-speaker conversation. Below are memories retrieved separately from each speaker's personal memory store; treat every speaker's evidence as equally authoritative and combine across speakers when the question requires it."

// dualSpeakerNowOverrideEnv is the operator-pin env that forces a fixed
// "Current time:" anchor regardless of retrieved memories. Mirrors the
// LOCOMO_CONV_NOW harness override so replay/eval scenarios can bolt
// the reference time without writing it into every payload.
const dualSpeakerNowOverrideEnv = "MEMDB_DUAL_NOW_OVERRIDE"

// dualSpeakerPromptTrailer nudges the LLM to resolve relative phrases
// against per-memory ts prefixes rather than the Current time header.
// M12.1 anti-poisoning: harness path emits the same trailer.
const dualSpeakerPromptTrailer = "Each memory line is prefixed with its in-conversation date (YYYY-MM-DD). " +
	"Resolve relative phrases like 'last week', 'yesterday', 'next month' against the dated memory, " +
	"NOT against the 'Current time' header. When asked WHEN an event happened, answer with the most " +
	"specific date or relative phrase present in the memories themselves."

// tsLookupKeys is the priority chain for extracting an in-conversation
// date from a memory item. observation_date wins because the M12.1 add
// pipeline writes it as the latest source message's chat_time and it
// survives the FormatMemoryItem sources strip. chat_time is kept as a
// forward-compat fallback; the rest are legacy keys.
var tsLookupKeys = [...]string{
	"observation_date", "chat_time", "created_at", "created_time", "time", "date",
}

// extractMemoryTs returns the in-conversation date for a single memory
// (YYYY-MM-DD), or "" when no usable string is found.
//
// Search results carry their props under metadata.* (FormatMemoryItem
// shape). Some test/synthetic callers pass a flat map, so the function
// falls back to the top-level map when metadata is absent.
func extractMemoryTs(m map[string]any) string {
	md, _ := m["metadata"].(map[string]any)
	if md == nil {
		md = m
	}
	for _, k := range tsLookupKeys {
		v, ok := md[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		if len(s) >= 10 {
			return s[:10]
		}
		return s
	}
	return ""
}

// dualPromptBlock is the rendered prompt block paired with the maximum
// ts seen across legs (for the "Current time:" anchor).
type dualPromptBlock struct {
	body  string
	maxTs string
}

// buildDualSpeakerPromptBlock renders a "## Speaker <id> memories: ..."
// block per speaker with M12.1 ts prefixes, returning the body and the
// max ts across all memory rows.
//
// Empty legs (search failed or returned 0) render as "(no memories
// retrieved)" so the model sees the explicit absence — matches the
// harness behaviour.
func buildDualSpeakerPromptBlock(legs []chatDualSpeakerLeg) (string, string) {
	out := renderDualBlock(legs)
	return out.body, out.maxTs
}

// renderDualBlock is the private worker — same output as the public
// pair-return helper but bundled in a struct for easier extension.
func renderDualBlock(legs []chatDualSpeakerLeg) dualPromptBlock {
	if len(legs) == 0 {
		return dualPromptBlock{}
	}
	var sb strings.Builder
	maxTs := ""
	for _, leg := range legs {
		sb.WriteString(fmt.Sprintf("## Speaker %s memories:\n", leg.speaker))
		if leg.err != nil || len(leg.memories) == 0 {
			sb.WriteString("(no memories retrieved)\n\n")
			continue
		}
		for i, m := range leg.memories {
			text, _ := m["memory"].(string)
			if text == "" {
				continue
			}
			ts := extractMemoryTs(m)
			if ts != "" {
				sb.WriteString(fmt.Sprintf("%d. %s: %s\n", i+1, ts, text))
				if ts > maxTs {
					maxTs = ts
				}
			} else {
				sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
			}
		}
		sb.WriteString("\n")
	}
	return dualPromptBlock{
		body:  strings.TrimRight(sb.String(), "\n"),
		maxTs: maxTs,
	}
}

// resolveDualConvNow picks the Current time: anchor for the dual-speaker
// prompt: env override > max retrieved ts > "" (omit).
//
// Never falls back to time.Now() — that was the M11 regression vector
// documented in docs/superpowers/plans/2026-04-29-m12-recovery-analysis.md.
func resolveDualConvNow(maxTs string) string {
	if v := strings.TrimSpace(os.Getenv(dualSpeakerNowOverrideEnv)); v != "" {
		return v
	}
	return maxTs
}

// composeDualSpeakerSystemPrompt assembles the full system prompt for a
// dual-speaker chat call when the caller did NOT supply a custom
// system_prompt. Order: header → "Current time: <ts>" anchor →
// per-speaker blocks → trailer with M12.1 anti-poisoning instruction.
//
// Returns the header alone when legs are empty AND no env override is
// set — back-compat with pre-M12.1 callers that used a bare header.
func composeDualSpeakerSystemPrompt(legs []chatDualSpeakerLeg) string {
	rendered := renderDualBlock(legs)
	convNow := resolveDualConvNow(rendered.maxTs)

	// Fast path: nothing to prepend or append → bare header.
	if convNow == "" && rendered.body == "" {
		return dualSpeakerChatPromptHeader
	}

	var sb strings.Builder
	sb.WriteString(dualSpeakerChatPromptHeader)
	if convNow != "" {
		sb.WriteString("\n\nCurrent time: ")
		sb.WriteString(convNow)
	}
	if rendered.body != "" {
		sb.WriteString("\n\n")
		sb.WriteString(rendered.body)
	}
	sb.WriteString("\n\n")
	sb.WriteString(dualSpeakerPromptTrailer)
	return sb.String()
}
