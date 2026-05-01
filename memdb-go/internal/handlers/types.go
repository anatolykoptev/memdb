package handlers

// types.go — typed request structs for all handler endpoints.
// These mirror the generated OpenAPI types but only include fields we validate.
// Using separate structs avoids coupling to oapi-codegen union types that are
// difficult to unmarshal directly.

import "encoding/json"

// searchRequest validates POST /product/search.
type searchRequest struct {
	Query   *string `json:"query"`
	UserID  *string `json:"user_id"`
	AgentID *string `json:"agent_id,omitempty"`
	Profile *string `json:"profile,omitempty"`
	TopK    *int    `json:"top_k,omitempty"`
	Dedup   *string `json:"dedup,omitempty"`

	Relativity   *float64 `json:"relativity,omitempty"`
	PrefTopK     *int     `json:"pref_top_k,omitempty"`
	ToolMemTopK  *int     `json:"tool_mem_top_k,omitempty"`
	SkillMemTopK *int     `json:"skill_mem_top_k,omitempty"`

	// Fields for native search handler proxy-fallback decisions
	Mode             *string   `json:"mode,omitempty"`
	InternetSearch   *bool     `json:"internet_search,omitempty"`
	ReadableCubeIDs  *[]string `json:"readable_cube_ids,omitempty"`
	IncludeEmbedding *bool     `json:"include_embedding,omitempty"`

	// Iterative expansion stages (0 = disabled, 2 = fast, 3 = fine)
	NumStages *int  `json:"num_stages,omitempty"`
	LLMRerank *bool `json:"llm_rerank,omitempty"`

	// Per-type gating
	IncludeSkillMemory *bool `json:"include_skill_memory,omitempty"`
	IncludePreference  *bool `json:"include_preference,omitempty"`
	SearchToolMemory   *bool `json:"search_tool_memory,omitempty"`

	// Level restricts search to a MemOS memory tier: l1, l2, or l3.
	// Omit (nil) for full search (backward compat).
	Level *string `json:"level,omitempty"`

	// Speakers — server-side dual/multi-speaker fan-out (M9 dual-speaker
	// retrieval). When non-empty, the handler issues one search per speaker
	// (each value is a user_id, identical to the existing UserID semantics)
	// in parallel, tags every returned memory with metadata.speaker_label,
	// and merges the per-speaker buckets into a single TopK list.
	//
	// When empty (or len==1 and identical to UserID): the request is
	// equivalent to the legacy single-speaker path — zero behaviour change
	// for existing callers. The eval-only client wrapper in
	// evaluation/locomo/query.py:query_search_dual is the reference shape;
	// vaelor and other prod clients can now opt in via the JSON field.
	Speakers []string `json:"speakers,omitempty"`

	// TopKPerSpeaker — per-speaker TopK budget when Speakers fan-out is
	// active. Defaults to TopK when nil/0. The merged response is still
	// capped at TopK so downstream callers see the same overall budget.
	TopKPerSpeaker *int `json:"top_k_per_speaker,omitempty"`

	// MergeStrategy — how per-speaker buckets are stitched into the final
	// list. Allowed values:
	//   - ""           → default (interleave)
	//   - "interleave" → round-robin over speakers (preserves diversity)
	//   - "score"      → flat sort by metadata.relativity descending
	// Unknown values are rejected at validation time. Ignored when
	// Speakers is empty.
	MergeStrategy *string `json:"merge_strategy,omitempty"`

	// AttributedTo — single-user filter by atomic-fact speaker attribution.
	// Distinct from Speakers (which fans out across user_ids). This filters
	// WITHIN a single user's memory pool to facts whose top-level
	// properties->>'attributed_to' = AttributedTo. Backed by the partial
	// index `idx_memory_attributed_to` from migration 0022. Used when one
	// user_id contains multiple speakers' atomic facts (e.g. dual-ingest
	// without reverse_role) and the caller wants a single perspective.
	// Empty/nil = no filter (legacy behaviour).
	AttributedTo *string `json:"attributed_to,omitempty"`

	// Locale — explicit BCP-47 language tag for D10 prompt selection.
	// When nil or empty, the server auto-detects from query text via
	// internal/lang.Detect. Supported values: "en", "ru", "zh".
	// Omit for legacy behaviour (auto-detect, backward compat).
	Locale *string `json:"locale,omitempty"`
}

// addRequest validates POST /product/add (basic fields only, used by ValidatedAdd).
type addRequest struct {
	UserID    *string `json:"user_id"`
	AgentID   *string `json:"agent_id,omitempty"`
	AsyncMode *string `json:"async_mode,omitempty"`
	Mode      *string `json:"mode,omitempty"`
}

// fullAddRequest is the complete POST /product/add request for the native handler.
type fullAddRequest struct {
	UserID    *string       `json:"user_id"`
	AgentID   *string       `json:"agent_id,omitempty"`
	AsyncMode *string       `json:"async_mode,omitempty"`
	Mode      *string       `json:"mode,omitempty"`
	Messages  []chatMessage `json:"messages,omitempty"`
	// WindowChars sets the approximate character budget per sliding window for
	// mode=fast/async ingest pipelines. Allowed range: [128, 16384]. Default
	// (when nil or out-of-range): 4096.
	//
	// Latency trade-off: each window triggers a separate embed call. Smaller
	// windows produce more memories at finer granularity (better retrieval recall
	// for QA workloads) but linear latency growth. At window=512 with a 30-msg
	// 1710-char conversation, p95 add latency rose from 1.2s to 20s (+1551%) on
	// 2026-04-25 — see docs/perf/2026-04-25-m7-latency-report.md. After embed
	// batching (M7 F2 follow-up) the cliff drops to ~1.5×. Recommended for
	// latency-sensitive paths: WindowChars >= 1024 OR rely on the default.
	//
	// Ignored by mode=raw, mode=fine, default (buffer) mode, and the feedback
	// path — those don't use sliding-window extraction.
	WindowChars     *int           `json:"window_chars,omitempty"`
	WritableCubeIDs []string       `json:"writable_cube_ids,omitempty"`
	SessionID       *string        `json:"session_id,omitempty"`
	CustomTags      []string       `json:"custom_tags,omitempty"`
	Info            map[string]any `json:"info,omitempty"`
	IsFeedback      *bool          `json:"is_feedback,omitempty"`
	TaskID          *string        `json:"task_id,omitempty"`
	// Key is an optional caller-supplied stable identifier stored in
	// properties.key (default ""). Used by the Anthropic memory-tool adapter
	// to address memories by hierarchical paths like "/memories/foo.txt".
	// Validation: max 512 chars, no NUL bytes. See validateAddRequest.
	Key *string `json:"key,omitempty"`
}

// chatMessage represents a single message in the add request.
type chatMessage struct {
	Role     string `json:"role"`
	Content  string `json:"content"`
	ChatTime string `json:"chat_time,omitempty"`
	// UUID is a caller-supplied stable identifier for this message. When
	// present it propagates into sources[].uuid so future ingests of the
	// same message (replay/retry/edit drift) can be deduplicated on a
	// stronger key than content_hash. Optional. Empty preserves legacy
	// content-hash-only behaviour byte-for-byte.
	UUID string `json:"uuid,omitempty"`
	// AgentID is per-message override for top-level agent_id. Useful when
	// a single transcript batch carries replies from multiple models
	// (main thread + subagent inline). Empty falls back to top-level.
	AgentID string `json:"agent_id,omitempty"`
	// Alias is the speaker's display name (e.g. "Alice", "Maria"). When
	// present it gets baked into the conversation text the LLM sees as
	// "Alice(user): ..." — Memobase pattern that lets the extractor write
	// self-attributed facts ("Alice mentioned X") without a separate
	// fan-out. Optional; if absent the bare role label is used.
	Alias string `json:"alias,omitempty"`
}

// feedbackRequest validates POST /product/feedback.
type feedbackRequest struct {
	UserID          *string          `json:"user_id"`
	AgentID         *string          `json:"agent_id,omitempty"`
	FeedbackContent *string          `json:"feedback_content"`
	History         *json.RawMessage `json:"history"`
}

// deleteRequest validates POST /product/delete_memory.
type deleteRequest struct {
	AgentID   *string                `json:"agent_id,omitempty"`
	MemoryIDs *[]string              `json:"memory_ids"`
	FileIDs   *[]string              `json:"file_ids"`
	Filter    map[string]interface{} `json:"filter"`
}

// getAllRequest validates POST /product/get_all.
type getAllRequest struct {
	UserID     *string `json:"user_id"`
	AgentID    *string `json:"agent_id,omitempty"`
	MemoryType *string `json:"memory_type"`
}

// chatCompleteRequest validates POST /product/chat/complete.
type chatCompleteRequest struct {
	UserID  *string `json:"user_id"`
	AgentID *string `json:"agent_id,omitempty"`
	Query   *string `json:"query"`
	TopK    *int    `json:"top_k,omitempty"`
}

// feedbackAddRecord is a single ADD operation result in the feedback response.
type feedbackAddRecord struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	SourceDocID string `json:"source_doc_id,omitempty"`
}

// feedbackUpdateRecord is a single UPDATE operation result in the feedback response.
type feedbackUpdateRecord struct {
	ID      string `json:"id"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// feedbackResponse mirrors Python mem_feedback response: {"record": {"add": [...], "update": [...]}}
type feedbackResponse struct {
	Record struct {
		Add    []feedbackAddRecord    `json:"add"`
		Update []feedbackUpdateRecord `json:"update"`
	} `json:"record"`
}

// chatRequest validates POST /product/chat and POST /product/chat/stream.
type chatRequest struct {
	UserID  *string `json:"user_id"`
	AgentID *string `json:"agent_id,omitempty"`
	Query   *string `json:"query"`
}

// getMemoryRequest validates POST /product/get_memory.
type getMemoryRequest struct {
	MemCubeID          *string                `json:"mem_cube_id"`
	UserID             *string                `json:"user_id,omitempty"`
	AgentID            *string                `json:"agent_id,omitempty"`
	IncludePreference  *bool                  `json:"include_preference,omitempty"`
	IncludeToolMemory  *bool                  `json:"include_tool_memory,omitempty"`
	IncludeSkillMemory *bool                  `json:"include_skill_memory,omitempty"`
	Filter             map[string]interface{} `json:"filter,omitempty"`
	Page               *int                   `json:"page,omitempty"`
	PageSize           *int                   `json:"page_size,omitempty"`
}

// getMemoryByIDsRequest validates POST /product/get_memory_by_ids.
type getMemoryByIDsRequest struct {
	MemoryIDs *[]string `json:"memory_ids"`
}

// existMemCubeRequest validates POST /product/exist_mem_cube_id.
type existMemCubeRequest struct {
	MemCubeID *string `json:"mem_cube_id"`
}

// nativeChatRequest is the full chat request for native Go handlers.
// Covers both /chat/complete and /chat/stream.
type nativeChatRequest struct {
	UserID             *string             `json:"user_id"`
	AgentID            *string             `json:"agent_id,omitempty"`
	Query              *string             `json:"query"`
	History            []map[string]string `json:"history,omitempty"`
	TopK               *int                `json:"top_k,omitempty"`
	Threshold          *float64            `json:"threshold,omitempty"`
	SystemPrompt       *string             `json:"system_prompt,omitempty"`
	ModelNameOrPath    *string             `json:"model_name_or_path,omitempty"`
	Mode               *string             `json:"mode,omitempty"`
	SessionID          *string             `json:"session_id,omitempty"`
	ReadableCubeIDs    []string            `json:"readable_cube_ids,omitempty"`
	WritableCubeIDs    []string            `json:"writable_cube_ids,omitempty"`
	IncludePreference  *bool               `json:"include_preference,omitempty"`
	PrefTopK           *int                `json:"pref_top_k,omitempty"`
	Filter             map[string]any      `json:"filter,omitempty"`
	AddMessageOnAnswer *bool               `json:"add_message_on_answer,omitempty"`
	MemCubeID          *string             `json:"mem_cube_id,omitempty"`
	InternetSearch     *bool               `json:"internet_search,omitempty"`

	// AnswerStyle selects the system-prompt template.
	// Allowed values:
	//   - ""               — default behaviour (cloudChatPromptEN/ZH), zero regression for existing clients.
	//   - "conversational" — explicit default; identical to "".
	//   - "factual"        — factual-extraction QA prompt. M12.2 splits this
	//                        into two confidence-conditional variants
	//                        (factualQAPromptHighConfidenceEN/ZH for top
	//                        score >= MEMDB_FACTUAL_CONFIDENCE_THRESHOLD,
	//                        factualQAPromptLowConfidenceEN/ZH otherwise).
	//                        Tuned for concise factual answers (e.g. LoCoMo
	//                        benchmark).
	// A non-empty SystemPrompt always wins over AnswerStyle (basePrompt path is preserved
	// for backward compatibility). Unknown values yield 400.
	AnswerStyle *string `json:"answer_style,omitempty"`

	// Level restricts memory search to a MemOS tier: l1, l2, or l3.
	// Omit (nil) for full search (backward compat).
	Level *string `json:"level,omitempty"`

	// Speakers — server-side dual/multi-speaker fan-out for chat retrieval
	// (M9 dual-speaker). When non-empty, chat issues one retrieval per
	// speaker in parallel (each is a user_id, identical UserID semantics),
	// tags every memory with metadata.speaker_label, then composes the
	// system prompt with per-speaker labelled memory blocks
	// ("## Speaker <id> memories: ..."). Mirrors the eval-only path in
	// evaluation/locomo/query.py:query_chat_dual but on the server, so
	// vaelor / other consumers can opt in without copying the harness.
	//
	// Empty (or len==1 == UserID): legacy single-speaker behaviour —
	// zero regression for existing callers.
	//
	// Compatibility: a non-empty SystemPrompt still wins (basePrompt
	// branch in buildSystemPromptWithDecision), but the system prompt
	// the handler builds itself when Speakers is set already includes
	// the per-speaker blocks.
	Speakers []string `json:"speakers,omitempty"`

	// TopKPerSpeaker — per-speaker TopK budget. Defaults to TopK.
	// Merged retrieval list is still capped at TopK overall.
	TopKPerSpeaker *int `json:"top_k_per_speaker,omitempty"`

	// MergeStrategy — same semantics as searchRequest.MergeStrategy.
	MergeStrategy *string `json:"merge_strategy,omitempty"`

	// MaxContextTokens — token budget for the memories block injected
	// into the system prompt. When set, formatMemories adds rows in
	// relativity-descending order until the budget is exhausted, then
	// stops (still respecting chatMinPersonalMem floor). When 0 / nil,
	// the legacy "all filtered memories pass through" behaviour stays
	// — zero regression for existing callers.
	//
	// Karpathy-style RAM management: prompt input has finite "L1 cache",
	// long unfiltered memory lists raise the noise floor and trigger the
	// rerank-gate "high-confidence" skip on padded pools. Capping the
	// memories block keeps the strongest evidence at the top of the
	// prompt and shortens latency.
	//
	// Approximate token counter: ~chars/4 (no real tokenizer dependency)
	// — within ±15% of tiktoken on English/Russian, plenty of headroom
	// for budget enforcement.
	MaxContextTokens *int `json:"max_context_tokens,omitempty"`
}
