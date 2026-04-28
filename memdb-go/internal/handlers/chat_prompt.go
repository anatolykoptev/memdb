package handlers

// chat_prompt.go — prompt templates and memory formatting for chat endpoints.

import (
	"context"
	"fmt"
	"os"
	"sort"
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
//  2. answerStyle == "factual" → factualQAPrompt<EN|ZH> chosen by detectLang(query).
//  3. otherwise → cloudChatPrompt<EN|ZH> (existing default).
//
// answerStyle values are validated upstream by validateChatRequest; this function
// treats any unknown value as the default branch (defensive — should never hit).
func buildSystemPrompt(query string, memories []map[string]any, prefString, basePrompt, answerStyle string) string {
	return buildSystemPromptWithProfile(context.TODO(), query, memories, prefString, basePrompt, answerStyle, "")
}

// buildSystemPromptWithProfile is the M10 Stream 3 variant that optionally
// prepends a "## User Profile" section to the rendered system prompt.
//
// profileSection — pre-rendered output of formatProfileSection (empty string
// means: do not prepend, e.g. when the env gate is disabled or the caller
// chose not to fetch profiles). When non-empty, the section is prepended with
// a blank line separator, BEFORE the existing memory section. The existing
// memory templates are not modified — this is strictly additive.
//
// The profile section is also prepended to custom basePrompt branches so the
// two-section ordering contract holds regardless of which template wins.
func buildSystemPromptWithProfile(_ context.Context, query string, memories []map[string]any, prefString, basePrompt, answerStyle, profileSection string) string {
	memCtx := formatMemories(memories, prefString)

	var rendered string
	switch {
	case basePrompt == "":
		lang := detectLang(query)
		tpl := cloudChatPromptEN
		if answerStyle == answerStyleFactual {
			tpl = factualQAPromptEN
			if lang == "zh" {
				tpl = factualQAPromptZH
			}
		} else if lang == "zh" {
			tpl = cloudChatPromptZH
		}
		now := chatPromptNow()
		rendered = fmt.Sprintf(tpl, now, memCtx)
	case strings.Contains(basePrompt, "{memories}"):
		rendered = strings.Replace(basePrompt, "{memories}", memCtx, 1)
	case len(memories) > 0:
		rendered = basePrompt + "\n\n## Fact Memories:\n" + memCtx
	default:
		rendered = basePrompt
	}

	if profileSection == "" {
		return rendered
	}
	return profileSection + "\n" + rendered
}

// formatMemories converts search result memories into numbered text for prompt injection.
func formatMemories(memories []map[string]any, prefString string) string {
	if len(memories) == 0 && prefString == "" {
		return ""
	}

	lines := make([]string, 0, len(memories))
	for i, m := range memories {
		text, _ := m["memory"].(string)
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, text))
	}

	out := strings.Join(lines, "\n")
	if prefString != "" {
		out += "\n\n" + prefString
	}
	return out
}

// filterMemoriesByThreshold filters memories by relativity score.
// Keeps all above threshold (OuterMemory excluded from the personal count),
// ensures minimum minNum personal results.
func filterMemoriesByThreshold(memories []map[string]any, threshold float64, minNum int) []map[string]any {
	if len(memories) == 0 {
		return nil
	}

	sorted := make([]map[string]any, len(memories))
	copy(sorted, memories)
	sortByRelativity(sorted)

	var personal, outer []map[string]any
	for _, m := range memories {
		if memType(m) == memTypeOuter {
			outer = append(outer, m)
		} else {
			personal = append(personal, m)
		}
	}

	var filtered []map[string]any
	perCount := 0
	for _, m := range sorted {
		if relativity(m) >= threshold {
			if memType(m) != memTypeOuter {
				perCount++
			}
			filtered = append(filtered, m)
		}
	}

	if len(filtered) < minNum {
		filtered = safeSlice(personal, minNum)
		filtered = append(filtered, safeSlice(outer, minNum)...)
	} else if perCount < minNum {
		filtered = append(filtered, personal[perCount:min(len(personal), minNum)]...)
	}

	sortByRelativity(filtered)
	return filtered
}

// --- helpers ---

func relativity(m map[string]any) float64 {
	if md, ok := m["metadata"].(map[string]any); ok {
		if v, ok := md["relativity"].(float64); ok {
			return v
		}
	}
	return 0
}

func memType(m map[string]any) string {
	if md, ok := m["metadata"].(map[string]any); ok {
		if v, ok := md["memory_type"].(string); ok {
			return v
		}
	}
	return ""
}

func sortByRelativity(s []map[string]any) {
	sort.Slice(s, func(i, j int) bool { return relativity(s[i]) > relativity(s[j]) })
}

func safeSlice(s []map[string]any, n int) []map[string]any {
	if n > len(s) {
		n = len(s)
	}
	out := make([]map[string]any, n)
	copy(out, s[:n])
	return out
}
