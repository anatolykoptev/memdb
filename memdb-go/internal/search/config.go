// Package search — shared configuration for search handlers.
package search

import "time"

// Default budgets per memory type.
const (
	DefaultTextTopK  = 10
	DefaultSkillTopK = 3
	DefaultPrefTopK  = 6
	DefaultToolTopK  = 6
	InflateFactor    = 5    // inflate top_k for dedup modes
	MinPrefLen       = 30   // minimum preference content length
	DefaultRelativity = 0.5 // minimum relevance threshold (filters noise from search results)
	CacheTTL         = 30 * time.Second

	// MMR tuning.
	// LangChain and MemOS default lambda=0.5 (pure balance).
	// We bias toward relevance at 0.7: 70% relevance, 30% diversity.
	DefaultMMRLambda = 0.7
	// Exponential penalty multiplier for similarities above 0.9.
	// At alpha=5: sim=0.95 → exp(5*0.05)=1.28x, diversity=1.22 (soft block).
	// At alpha=10 (old): sim=0.95 → exp(10*0.05)=1.65x, diversity=1.57 (hard block).
	DefaultMMRAlpha = 5.0

	// Temporal decay v2 — weighted combination: final = SemanticWeight*cosine + RecencyWeight*recency.
	// recency = exp(-DecayAlpha * days_since_last_access)  [fallback: days_since_created]
	//
	// Weights (must sum to 1.0):
	//   MemOS uses: 0.6 semantic + 0.3 recency + 0.1 importance
	//   mem0 uses:  0.7 relevance + 0.3 recency
	//   We use:     0.75 semantic + 0.25 recency (relevance-first, recency as tiebreaker)
	DecaySemanticWeight = 0.75
	DecayRecencyWeight  = 0.25

	// DecayAlpha: half-life = ln(2)/alpha.
	// 0.0039 → half-life ~180 days (recency=0.5 at 180 days old).
	// Set DecayAlpha=-1 in SearchParams to disable decay entirely.
	DefaultDecayAlpha = 0.0039 // half-life ~180 days

	// MaxDecayAgeDays: memories older than this get recency=0 (semantic score still counts).
	// MemOS uses 365. We use 730 (2 years) — gentler cutoff.
	MaxDecayAgeDays = 730
)

// Default limits for graph recall and working memory.
const (
	GraphRecallLimit    = 50   // max candidates from graph recall (before merge)
	WorkingMemoryLimit  = 20   // max WorkingMemory items
	GraphKeyScore       = 0.85 // fixed score for key-based graph recall
	GraphTagBaseScore   = 0.70 // base score for tag-based graph recall
	GraphTagBonusPerTag = 0.05 // bonus per overlapping tag
	WorkingMemBaseScore = 0.80 // fallback score for WorkingMemory without embedding
	WorkingMemMaxScore  = 0.92 // cap for WorkingMemory — prevents 1.00 domination
)

// Memory scopes — separated by type so each gets its own budget.
var TextScopes = []string{"LongTermMemory", "UserMemory", "EpisodicMemory"}
var SkillScopes = []string{"SkillMemory"}
var ToolScopes = []string{"ToolSchemaMemory", "ToolTrajectoryMemory"}

// GraphRecallScopes are the scopes searched by graph-based recall (key/tag match).
var GraphRecallScopes = []string{"LongTermMemory", "UserMemory", "SkillMemory", "EpisodicMemory"}

// EpisodicScopes restricts vector search to episodic memories only (MemOS L2).
var EpisodicScopes = []string{"EpisodicMemory"}

// Internet search defaults.
const (
	DefaultInternetLimit = 5   // max web results to embed and merge
	InternetBaseScore    = 0.5 // base score for internet results before reranking
)

// PrefCollections are the Qdrant collections for preference memory.
var PrefCollections = []string{"explicit_preference", "implicit_preference"}
