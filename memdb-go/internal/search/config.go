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

// Default limits for graph recall and working memory.
const (
	GraphRecallLimit    = 50   // max candidates from graph recall (before merge)
	WorkingMemoryLimit  = 20   // max WorkingMemory items
	GraphKeyScore       = 0.85 // fixed score for key-based graph recall
	GraphTagBaseScore   = 0.70 // base score for tag-based graph recall
	GraphTagBonusPerTag = 0.05 // bonus per overlapping tag
	WorkingMemBaseScore = 0.80 // fallback score for WorkingMemory without embedding
)

// Memory scopes — separated by type so each gets its own budget.
var TextScopes = []string{"LongTermMemory", "UserMemory"}
var SkillScopes = []string{"SkillMemory"}
var ToolScopes = []string{"ToolSchemaMemory", "ToolTrajectoryMemory"}

// GraphRecallScopes are the scopes searched by graph-based recall (key/tag match).
var GraphRecallScopes = []string{"LongTermMemory", "UserMemory", "SkillMemory"}

// PrefCollections are the Qdrant collections for preference memory.
var PrefCollections = []string{"explicit_preference", "implicit_preference"}
