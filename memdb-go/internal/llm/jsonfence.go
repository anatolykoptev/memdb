// Package llm — jsonfence.go provides a helper to strip markdown code fences
// from LLM responses before json.Unmarshal. LLMs frequently wrap JSON output
// in ```json ... ``` fences; without stripping, Unmarshal fails on the backtick.
package llm

import (
	"bytes"
	"regexp"
)

// fencedJSON matches a markdown code fence optionally tagged with `json`.
// Handles LF and CRLF line endings, leading/trailing whitespace, and
// the bare ``` form (no language tag).
var fencedJSON = regexp.MustCompile("(?s)^\\s*```(?:json)?\\s*\\n?(.*?)\\n?\\s*```\\s*$")

// StripJSONFence removes a surrounding markdown code fence if present.
// Returns the trimmed input unchanged if no fence is found. Safe to call
// on any LLM response bytes before json.Unmarshal.
func StripJSONFence(b []byte) []byte {
	if m := fencedJSON.FindSubmatch(b); m != nil {
		return bytes.TrimSpace(m[1])
	}
	return bytes.TrimSpace(b)
}
