// Package search — shared configuration for search handlers.
package search

import "time"

// Default budgets per memory type.
const (
	DefaultTextTopK  = 10
	DefaultSkillTopK = 3
	DefaultPrefTopK  = 6
	DefaultToolTopK  = 6
	InflateFactor    = 5 // inflate top_k for dedup modes
	MinPrefLen       = 30 // minimum preference content length
	CacheTTL         = 30 * time.Second
)

// Memory scopes — separated by type so each gets its own budget.
var TextScopes = []string{"LongTermMemory", "UserMemory"}
var SkillScopes = []string{"SkillMemory"}
var ToolScopes = []string{"ToolSchemaMemory", "ToolTrajectoryMemory"}

// PrefCollections are the Qdrant collections for preference memory.
var PrefCollections = []string{"explicit_preference", "implicit_preference"}
