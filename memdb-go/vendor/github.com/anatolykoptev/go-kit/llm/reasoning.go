package llm

import "strings"

// splitReasoning separates a reasoning-model's chain-of-thought from the
// answer. Modern reasoning models surface thinking two ways and the wire
// shape is decided by the SERVING stack's reasoning-parser, not the model,
// so we defend against both:
//  1. inline `<think>…</think>` at the START of content (parser disabled);
//  2. a sibling `reasoning_content` field (parser enabled).
//
// Returns (clean, reasoning): clean is the answer safe to json.Unmarshal,
// reasoning is the merged chain-of-thought (reasoning_content first, then
// inline). Only a LEADING <think> is stripped — a tag later in content is
// left intact (could be legitimate content/code).
func splitReasoning(content, reasoningContent string) (clean, reasoning string) {
	reasoning = reasoningContent
	c := strings.TrimSpace(content)
	if strings.HasPrefix(c, "<think>") {
		inner := c[len("<think>"):]
		if end := strings.Index(inner, "</think>"); end >= 0 {
			inline := strings.TrimSpace(inner[:end])
			clean = strings.TrimSpace(inner[end+len("</think>"):])
			reasoning = mergeReasoning(reasoning, inline)
		} else {
			// Unclosed: the whole tail is thinking, no answer arrived.
			reasoning = mergeReasoning(reasoning, strings.TrimSpace(inner))
			clean = ""
		}
		return clean, reasoning
	}
	return c, reasoning
}

func mergeReasoning(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n" + b
	}
}
