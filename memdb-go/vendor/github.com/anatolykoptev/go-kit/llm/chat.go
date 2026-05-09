package llm

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
)

// Message is a chat message.
//
// ChatTime, MessageID, and Name are wire-compatible additions aligned
// with the MemDB ingestion schema (api/openapi.yaml on memdb-go) so a
// service can emit one Message and route it both to the LLM and to
// MemDB without per-system reshaping. All three fields are
// `omitempty`, byte-identical to today on default-zero calls.
//
// Provider safety: OpenAI's /v1/chat/completions and Anthropic's
// /v1/messages ignore unknown top-level keys on message objects (same
// guarantee that lets ContentPart.CacheControl flow through). `name`
// is OpenAI-native; `chat_time` and `message_id` are MemDB-aligned
// snake_case names that providers silently drop.
//
// To make ChatTime visible to the model, pair it with the
// WithMessageTimestamps ChatOption — it prepends a bracketed UTC
// timestamp to text Content right before send. Without that option
// the field is wire-only metadata invisible to the LLM.
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"` // string or []ContentPart for multimodal
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`

	// ChatTime is the message timestamp in RFC3339 (e.g. "2026-05-04T06:30:00Z").
	// MemDB indexes the first 10 chars (YYYY-MM-DD) as observation_date and
	// keeps the full string in properties.chat_time. Empty = no timestamp.
	ChatTime string `json:"chat_time,omitempty"`

	// MessageID is a stable per-message identifier. Used by MemDB for
	// dedup. Optional. Empty = let the upstream system assign.
	MessageID string `json:"message_id,omitempty"`

	// Name is an optional speaker label (OpenAI-native; MemDB-honoured).
	// Useful for multi-party conversations or attributing system blocks.
	Name string `json:"name,omitempty"`
}

// ContentPart is a part of a multimodal message.
//
// CacheControl is honoured by Anthropic-compatible APIs as a prompt-cache
// breakpoint; see CacheControl docs in cache.go. Other providers ignore
// the unknown field, so it is safe to always send.
type ContentPart struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	ImageURL     *ImageURL     `json:"image_url,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// ImageURL holds an image reference for vision requests.
type ImageURL struct {
	URL string `json:"url"`
}

// ImagePart is a convenience type for passing images to CompleteMultimodal.
type ImagePart struct {
	URL      string
	MIMEType string // optional
}

// ChatRequest is a chat completion request. Exported for use with Middleware.
//
// Temperature is a pointer so it can be omitted from the request body when
// nil — Anthropic's claude-opus-4-7 (and likely future variants) rejects
// `temperature` entirely with `400 invalid_request_error`. Passing a pointer
// keeps backward compatibility for callers who set it explicitly via
// WithChatTemperature, while letting the request omit the field when no
// override was requested.
type ChatRequest struct {
	Model          string    `json:"model"`
	Messages       []Message `json:"messages"`
	Temperature    *float64  `json:"temperature,omitempty"`
	MaxTokens      int       `json:"max_tokens"`
	Stream         bool      `json:"stream,omitempty"`
	Tools          []Tool    `json:"tools,omitempty"`
	ToolChoice     any       `json:"tool_choice,omitempty"`
	ResponseFormat any       `json:"response_format,omitempty"`
}

// Usage holds token usage from the API response.
//
// The struct is shape-tolerant: UnmarshalJSON (in cache.go) reads BOTH
// OpenAI's {prompt_tokens, completion_tokens, total_tokens,
// prompt_tokens_details.cached_tokens} and Anthropic's {input_tokens,
// output_tokens, cache_read_input_tokens, cache_creation_input_tokens}
// shapes and normalises into the fields below.
//
// CachedTokens is the count served from the prompt cache on this call —
// emit as a span/metric attribute to verify caching is actually working
// in production. CacheCreationTokens is Anthropic-only (tokens written
// to the cache; OpenAI's automatic caching does not separate creation).
type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	CachedTokens        int `json:"cached_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
}

// Tool defines a function tool for the API.
//
// CacheControl marks the tool definition as a cache breakpoint (Anthropic
// only — useful when a long, stable tool catalog is sent on every turn).
// Set on the LAST tool in the slice to cache the cumulative tools prefix.
type Tool struct {
	Type         string        `json:"type"`
	Function     ToolFunction  `json:"function"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// ToolFunction describes a callable function.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// NewTool creates a function tool with the given name, description, and JSON Schema parameters.
func NewTool(name, description string, parameters any) Tool {
	return Tool{
		Type:     "function",
		Function: ToolFunction{Name: name, Description: description, Parameters: parameters},
	}
}

// ToolCall represents a tool call from the assistant response.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the function name and JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatResponse is the full response from Chat.
type ChatResponse struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        *Usage
}

// ChatOption configures a per-request Chat option.
type ChatOption func(*chatConfig)

type chatConfig struct {
	tools             []Tool
	toolChoice        any
	responseFormat    any
	temperature       *float64
	maxTokens         *int
	timestampMessages bool
}

func (cfg *chatConfig) apply(req *ChatRequest) {
	if cfg.tools != nil {
		req.Tools = cfg.tools
	}
	if cfg.toolChoice != nil {
		req.ToolChoice = cfg.toolChoice
	}
	if cfg.responseFormat != nil {
		req.ResponseFormat = cfg.responseFormat
	}
	if cfg.temperature != nil {
		t := *cfg.temperature
		req.Temperature = &t
	}
	if cfg.maxTokens != nil {
		req.MaxTokens = *cfg.maxTokens
	}
	if cfg.timestampMessages {
		applyMessageTimestamps(req.Messages)
	}
}

// WithTools sets the available tools for the request.
func WithTools(tools []Tool) ChatOption {
	return func(c *chatConfig) { c.tools = tools }
}

// WithToolChoice sets the tool choice strategy ("auto", "none", or a specific tool).
func WithToolChoice(choice any) ChatOption {
	return func(c *chatConfig) { c.toolChoice = choice }
}

// WithChatTemperature overrides the sampling temperature for a single call.
func WithChatTemperature(t float64) ChatOption {
	return func(c *chatConfig) { c.temperature = &t }
}

// WithChatMaxTokens overrides the max tokens for a single call.
func WithChatMaxTokens(n int) ChatOption {
	return func(c *chatConfig) { c.maxTokens = &n }
}

// WithMessageTimestamps prepends a bracketed UTC timestamp to each
// Message.Content (string-typed only) for messages whose ChatTime is
// non-empty. Format: "[YYYY-MM-DD HH:MM UTC] <original content>".
//
// Pair with Message.ChatTime to give the LLM time-awareness without
// leaking timestamps into messages that were not authored at a
// specific moment (system blocks, tool results — leave ChatTime "").
//
// Multimodal messages (Content is []ContentPart) are NOT modified to
// avoid disturbing image_url shapes; if you need timestamped multimodal,
// prepend a leading text ContentPart yourself.
//
// Opt-in. Off by default — calls remain byte-identical to before.
func WithMessageTimestamps() ChatOption {
	return func(c *chatConfig) { c.timestampMessages = true }
}

// WithJSONSchema sets the response format to structured JSON output.
func WithJSONSchema(name string, schema any) ChatOption {
	return func(c *chatConfig) {
		c.responseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"strict": true,
				"schema": schema,
			},
		}
	}
}

// Chat sends a chat completion request and returns the full response
// including tool calls, finish reason, and token usage.
func (c *Client) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*ChatResponse, error) {
	var cfg chatConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	req := c.newRequest(messages)
	cfg.apply(req)
	return c.execute(ctx, req)
}

// ChatTyped sends a structured output request and unmarshals the response into target.
// Generates JSON Schema from target's type, sends it as response_format,
// and unmarshals the JSON response directly into target.
func (c *Client) ChatTyped(ctx context.Context, messages []Message, target any) error {
	schema := SchemaOf(target)
	t := reflect.TypeOf(target)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name := strings.ToLower(t.Name())
	if name == "" {
		name = "response"
	}

	resp, err := c.Chat(ctx, messages, WithJSONSchema(name, schema))
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(resp.Content), target)
}
