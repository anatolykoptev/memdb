// Package mcptools defines MCP tool input/output types and registration for the MemDB MCP server.
package mcptools

// --- Search tool ---

// SearchInput maps to the Python search_memories MCP tool parameters.
type SearchInput struct {
	Query      string   `json:"query" jsonschema:"Search query text"`
	UserID     string   `json:"user_id,omitempty" jsonschema:"User ID for memory scoping"`
	CubeIDs    []string `json:"cube_ids,omitempty" jsonschema:"List of cube IDs to search in"`
	Profile    string   `json:"profile,omitempty" jsonschema:"Search profile preset: inject, default, deep"`
	TopK       int      `json:"top_k,omitempty" jsonschema:"Max results per category (default 6)"`
	Relativity float64  `json:"relativity,omitempty" jsonschema:"Minimum relevance score threshold 0-1 (default 0.85)"`
	Dedup      string   `json:"dedup,omitempty" jsonschema:"Dedup mode: no, sim, mmr (default mmr)"`
}

// --- Memory CRUD tools ---

// GetMemoryInput maps to the Python get_memory MCP tool parameters.
type GetMemoryInput struct {
	CubeID   string `json:"cube_id" jsonschema:"Memory cube ID"`
	MemoryID string `json:"memory_id" jsonschema:"Memory node ID"`
	UserID   string `json:"user_id,omitempty" jsonschema:"User ID"`
}

// UpdateMemoryInput maps to the Python update_memory MCP tool parameters.
type UpdateMemoryInput struct {
	CubeID        string `json:"cube_id" jsonschema:"Memory cube ID"`
	MemoryID      string `json:"memory_id" jsonschema:"Memory node ID"`
	MemoryContent string `json:"memory_content" jsonschema:"New memory content"`
	UserID        string `json:"user_id,omitempty" jsonschema:"User ID"`
}

// DeleteMemoryInput maps to the Python delete_memory MCP tool parameters.
type DeleteMemoryInput struct {
	CubeID   string `json:"cube_id" jsonschema:"Memory cube ID"`
	MemoryID string `json:"memory_id" jsonschema:"Memory node ID"`
	UserID   string `json:"user_id,omitempty" jsonschema:"User ID"`
}

// DeleteAllMemoriesInput maps to the Python delete_all_memories MCP tool parameters.
type DeleteAllMemoriesInput struct {
	CubeID string `json:"cube_id" jsonschema:"Memory cube ID"`
	UserID string `json:"user_id,omitempty" jsonschema:"User ID"`
}

// --- User tools ---

// CreateUserInput maps to the Python create_user MCP tool parameters.
type CreateUserInput struct {
	UserID   string `json:"user_id" jsonschema:"Unique user identifier"`
	Role     string `json:"role,omitempty" jsonschema:"User role: USER or ADMIN"`
	UserName string `json:"user_name,omitempty" jsonschema:"Display name"`
}

// GetUserInfoInput maps to the Python get_user_info MCP tool parameters.
type GetUserInfoInput struct {
	UserID string `json:"user_id,omitempty" jsonschema:"User ID to look up"`
}

// --- Proxy tool (generic) ---

// ProxyInput captures arbitrary JSON for tools proxied to Python.
type ProxyInput map[string]any

// --- Output types ---

// TextResult is a generic text output for MCP tools.
type TextResult struct {
	Result any `json:"result"`
}

// --- Proxy tool inputs (typed for proper JSON Schema generation) ---

// AddMemoryProxyInput for the add_memory proxy tool.
type AddMemoryProxyInput struct {
	UserID        string `json:"user_id" jsonschema:"User ID"`
	MemoryContent string `json:"memory_content,omitempty" jsonschema:"Direct text content to add as memory"`
	DocPath       string `json:"doc_path,omitempty" jsonschema:"Path to a document file to process"`
	MemCubeID     string `json:"mem_cube_id,omitempty" jsonschema:"Target cube ID"`
	Source        string `json:"source,omitempty" jsonschema:"Source of the memory"`
	SessionID     string `json:"session_id,omitempty" jsonschema:"Session ID"`
}

// ChatProxyInput for the chat proxy tool.
type ChatProxyInput struct {
	UserID       string `json:"user_id" jsonschema:"User ID for the chat session"`
	Query        string `json:"query" jsonschema:"Chat query or question"`
	MemCubeID    string `json:"mem_cube_id,omitempty" jsonschema:"Cube ID to use for chat"`
	SystemPrompt string `json:"system_prompt,omitempty" jsonschema:"System prompt for chat"`
}

// ClearChatHistoryProxyInput for the clear_chat_history proxy tool.
type ClearChatHistoryProxyInput struct {
	UserID string `json:"user_id,omitempty" jsonschema:"User ID whose chat history to clear"`
}

// CreateCubeProxyInput for the create_cube proxy tool.
type CreateCubeProxyInput struct {
	CubeName string `json:"cube_name" jsonschema:"Human-readable name for the memory cube"`
	OwnerID  string `json:"owner_id" jsonschema:"User ID of the cube owner"`
	CubePath string `json:"cube_path,omitempty" jsonschema:"File system path for cube data"`
	CubeID   string `json:"cube_id,omitempty" jsonschema:"Custom unique identifier for the cube"`
}

// RegisterCubeProxyInput for the register_cube proxy tool.
type RegisterCubeProxyInput struct {
	CubeNameOrPath string `json:"cube_name_or_path" jsonschema:"File path or name for the cube"`
	CubeID         string `json:"cube_id,omitempty" jsonschema:"Custom identifier for the cube"`
	UserID         string `json:"user_id,omitempty" jsonschema:"User ID to associate with the cube"`
}

// UnregisterCubeProxyInput for the unregister_cube proxy tool.
type UnregisterCubeProxyInput struct {
	CubeID string `json:"cube_id" jsonschema:"Unique identifier of the cube to unregister"`
	UserID string `json:"user_id,omitempty" jsonschema:"User ID for access validation"`
}

// ShareCubeProxyInput for the share_cube proxy tool.
type ShareCubeProxyInput struct {
	CubeID       string `json:"cube_id" jsonschema:"Unique identifier of the cube to share"`
	TargetUserID string `json:"target_user_id" jsonschema:"User ID to share the cube with"`
}

// DumpCubeProxyInput for the dump_cube proxy tool.
type DumpCubeProxyInput struct {
	DumpDir string `json:"dump_dir" jsonschema:"Directory path for cube export"`
	UserID  string `json:"user_id,omitempty" jsonschema:"User ID for access validation"`
	CubeID  string `json:"cube_id,omitempty" jsonschema:"Cube ID to export"`
}

// ControlSchedulerProxyInput for the control_memory_scheduler proxy tool.
type ControlSchedulerProxyInput struct {
	Action string `json:"action" jsonschema:"Action to perform: start or stop"`
}

// SearchMemoriesProxyInput for proxying search_memories to memdb-go /product/search.
type SearchMemoriesProxyInput struct {
	Query      string   `json:"query"`
	UserID     string   `json:"user_id"`
	Profile    string   `json:"profile,omitempty"`
	TopK       int      `json:"top_k,omitempty"`
	Relativity float64  `json:"relativity,omitempty"`
	Dedup      string   `json:"dedup,omitempty"`
	CubeIDs    []string `json:"readable_cube_ids,omitempty"`
}
