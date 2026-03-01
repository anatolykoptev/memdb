package handlers

// chat_prompt.go — prompt templates and memory formatting for chat endpoints.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// buildSystemPrompt constructs the chat system prompt with memory context.
// If basePrompt is non-empty, uses it; otherwise picks EN/ZH template by language.
func buildSystemPrompt(query string, memories []map[string]any, prefString, basePrompt string) string {
	memCtx := formatMemories(memories, prefString)

	if basePrompt == "" {
		lang := detectLang(query)
		tpl := cloudChatPromptEN
		if lang == "zh" {
			tpl = cloudChatPromptZH
		}
		now := time.Now().Format("2006-01-02 15:04 (Monday)")
		return fmt.Sprintf(tpl, now, memCtx)
	}

	if strings.Contains(basePrompt, "{memories}") {
		return strings.Replace(basePrompt, "{memories}", memCtx, 1)
	}
	if len(memories) > 0 {
		return basePrompt + "\n\n## Fact Memories:\n" + memCtx
	}
	return basePrompt
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
		if memType(m) == "OuterMemory" {
			outer = append(outer, m)
		} else {
			personal = append(personal, m)
		}
	}

	var filtered []map[string]any
	perCount := 0
	for _, m := range sorted {
		if relativity(m) >= threshold {
			if memType(m) != "OuterMemory" {
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
