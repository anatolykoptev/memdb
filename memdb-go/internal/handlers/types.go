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
}

// chatMessage represents a single message in the add request.
type chatMessage struct {
	Role     string `json:"role"`
	Content  string `json:"content"`
	ChatTime string `json:"chat_time,omitempty"`
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
	//   - "factual"        — factual-extraction QA prompt (factualQAPromptEN/ZH).
	//                        Tuned for short-phrase answers (e.g. LoCoMo benchmark).
	// A non-empty SystemPrompt always wins over AnswerStyle (basePrompt path is preserved
	// for backward compatibility). Unknown values yield 400.
	AnswerStyle *string `json:"answer_style,omitempty"`

	// Level restricts memory search to a MemOS tier: l1, l2, or l3.
	// Omit (nil) for full search (backward compat).
	Level *string `json:"level,omitempty"`
}
