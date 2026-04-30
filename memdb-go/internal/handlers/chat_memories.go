package handlers

// chat_memories.go — utilities for shaping the memory list before it
// reaches the chat prompt:
//
//   - formatMemories          renders memories into the numbered text block
//                             injected into the system prompt, optionally
//                             enforcing a token budget
//   - filterMemoriesByThreshold drops sub-threshold memories while honouring
//                             the chatMinPersonalMem floor and segregating
//                             OuterMemory from personal counts
//   - relativity / memType / sortByRelativity / safeSlice are the small
//     accessors and slice helpers used by both functions above
//
// Pulled out of chat_prompt.go (M12 token-budget refactor) so prompt
// assembly and memory-list shaping live in separate files. No behaviour
// change vs the prior monolithic file — the same package keeps every
// caller intact.

import (
	"fmt"
	"sort"
	"strings"
)

// formatMemories converts search result memories into numbered text for
// prompt injection.
//
// When maxTokens > 0, memories are added in relativity-descending order
// until the budget is exhausted; the floor of chatMinPersonalMem rows
// is always honoured even when it overshoots the budget (otherwise an
// empty memories block would force the model to refuse trivial
// questions). When maxTokens <= 0 the legacy "all rows pass through"
// behaviour is preserved for back-compat.
//
// approxTokens (chars/4) lives in chat_prompt_profile.go and is shared
// here — token counting is intentionally cheap so the budget enforcer
// adds no measurable overhead on the hot path.
func formatMemories(memories []map[string]any, prefString string, maxTokens int) string {
	if len(memories) == 0 && prefString == "" {
		return ""
	}

	lines := make([]string, 0, len(memories))
	if maxTokens > 0 && len(memories) > 0 {
		// Budget-aware path: sort by relativity desc, append until cap.
		sorted := make([]map[string]any, len(memories))
		copy(sorted, memories)
		sortByRelativity(sorted) // descending; see sortByRelativity helper
		used := 0
		for i, m := range sorted {
			text, _ := m["memory"].(string)
			line := fmt.Sprintf("%d. %s", i+1, text)
			cost := approxTokens(line)
			if used+cost > maxTokens && len(lines) >= chatMinPersonalMem {
				break
			}
			lines = append(lines, line)
			used += cost
		}
	} else {
		for i, m := range memories {
			text, _ := m["memory"].(string)
			lines = append(lines, fmt.Sprintf("%d. %s", i+1, text))
		}
	}

	out := strings.Join(lines, "\n")
	if prefString != "" {
		out += "\n\n" + prefString
	}
	return out
}

// filterMemoriesByThreshold filters memories by relativity score.
// Keeps all above threshold (OuterMemory excluded from the personal
// count), ensures minimum minNum personal results.
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

// ── small accessors & slice helpers ─────────────────────────────────

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
