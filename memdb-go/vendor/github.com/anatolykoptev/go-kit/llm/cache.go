package llm

import (
	"encoding/json"
)

// CacheControl is a prompt-cache marker. Compatible with Anthropic's
// cache_control field (the only major provider that accepts an explicit
// breakpoint today). When set on a ContentPart or Tool, Anthropic-
// compatible APIs cache the cumulative prefix UP TO AND INCLUDING that
// block; subsequent calls reading the same prefix are billed at ~10%
// of the input-token rate.
//
// OpenAI / Gemini-Compat / OpenRouter and most OpenAI-compatible
// proxies ignore unknown fields, so leaving CacheControl on outgoing
// requests to those providers is safe — they simply do not cache.
// (OpenAI's automatic prompt caching kicks in independently for prompts
// >=1024 tokens with stable prefixes.)
type CacheControl struct {
	// Type is the cache mode. "ephemeral" is the only value Anthropic
	// supports today.
	Type string `json:"type"`

	// TTL is the cache lifetime. Empty = provider default (5m for
	// Anthropic). Set "1h" to opt into Anthropic's extended-cache-ttl
	// beta (requires `extended-cache-ttl-2025-04-11` beta header on
	// the request — caller's responsibility).
	TTL string `json:"ttl,omitempty"`
}

// Ephemeral returns the standard 5-minute cache marker. Most callers
// should use this directly:
//
//	parts := []llm.ContentPart{
//	    {Type: "text", Text: bigSystemPrompt, CacheControl: llm.Ephemeral()},
//	}
func Ephemeral() *CacheControl {
	return &CacheControl{Type: "ephemeral"}
}

// EphemeralExtended returns a cache marker requesting Anthropic's 1-hour
// extended TTL. Caller must also send the
// `anthropic-beta: extended-cache-ttl-2025-04-11` header — pass via
// WithHTTPClient on a transport that injects it, or via the proxy.
func EphemeralExtended() *CacheControl {
	return &CacheControl{Type: "ephemeral", TTL: "1h"}
}

// NewCachedSystemMessage builds a system Message with the text wrapped
// in a single ContentPart carrying the given cache_control. This is the
// idiomatic way to mark a long, stable system prompt as cacheable for
// Anthropic without disturbing OpenAI-compat targets.
//
// If cc is nil, the returned Message is a plain string-content system
// message (no caching marker).
func NewCachedSystemMessage(text string, cc *CacheControl) Message {
	return cachedSimpleMessage("system", text, cc)
}

// NewCachedUserMessage is the user-role variant. Useful when a long
// retrieved-context block is appended to the user message and should be
// cached across follow-up turns.
func NewCachedUserMessage(text string, cc *CacheControl) Message {
	return cachedSimpleMessage("user", text, cc)
}

func cachedSimpleMessage(role, text string, cc *CacheControl) Message {
	if cc == nil {
		return Message{Role: role, Content: text}
	}
	return Message{
		Role: role,
		Content: []ContentPart{
			{Type: "text", Text: text, CacheControl: cc},
		},
	}
}

// usageRaw is the wire-shape Usage union of OpenAI and Anthropic field
// names. We unmarshal into this and then populate the public Usage with
// the normalised CachedTokens / CacheCreationTokens. Defining it here
// (next to CacheControl) keeps the cache-related concerns in one file.
//
// OpenAI shape (since 2024-10):
//
//	{"prompt_tokens": 1234, "completion_tokens": 56, "total_tokens": 1290,
//	 "prompt_tokens_details": {"cached_tokens": 1024}}
//
// Anthropic shape (since 2024-04 GA):
//
//	{"input_tokens": 1234, "output_tokens": 56,
//	 "cache_read_input_tokens": 1024,
//	 "cache_creation_input_tokens": 200}
//
// We honour both by reading whichever fields are populated. Callers
// observe the merged result via Usage.CachedTokens.
type usageRaw struct {
	// OpenAI field names
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptDetails    struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`

	// Anthropic field names
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// applyTo writes the normalised values into u. Prefers OpenAI counts
// when both are present (they're the more common path in our stack);
// Anthropic-only fields are read when the OpenAI counterparts are zero.
func (r *usageRaw) applyTo(u *Usage) {
	switch {
	case r.PromptTokens > 0:
		u.PromptTokens = r.PromptTokens
		u.CompletionTokens = r.CompletionTokens
		u.TotalTokens = r.TotalTokens
	case r.InputTokens > 0:
		u.PromptTokens = r.InputTokens
		u.CompletionTokens = r.OutputTokens
		u.TotalTokens = r.InputTokens + r.OutputTokens
	}
	switch {
	case r.PromptDetails.CachedTokens > 0:
		u.CachedTokens = r.PromptDetails.CachedTokens
	case r.CacheReadInputTokens > 0:
		u.CachedTokens = r.CacheReadInputTokens
	}
	u.CacheCreationTokens = r.CacheCreationInputTokens
}

// UnmarshalJSON allows Usage to absorb both OpenAI and Anthropic shapes
// transparently. Callers always read the normalised field names below.
func (u *Usage) UnmarshalJSON(b []byte) error {
	var raw usageRaw
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	raw.applyTo(u)
	return nil
}
