// Package mcptools defines MCP tool input/output types and registration for the MemDB MCP server.
package mcptools

// --- Search tool ---

// SearchInput maps to the Python search_memories MCP tool parameters.
type SearchInput struct {
	Query      string   `json:"query" jsonschema:"Search query text"`
	UserID     string   `json:"user_id,omitempty" jsonschema:"User ID for memory scoping"`
	CubeIDs    []string `json:"cube_ids,omitempty" jsonschema:"List of cube IDs to search in"`
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
