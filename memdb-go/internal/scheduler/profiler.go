package scheduler

// profiler.go — background user profile summary generation (Memobase-style).
//
// Memobase achieves 85% on LOCOMO temporal questions primarily because it maintains
// a structured user profile that is always injected as context regardless of the query.
// This profile contains long-lived facts (name, age, occupation, location, hobbies, etc.)
// that don't need to be retrieved — they're always present.
//
// Implementation:
//   - Triggered non-blocking after each fine-mode add via TriggerProfileRefresh
//   - Reads all UserMemory nodes for the cube via Postgres
//   - Asks LLM to extract a structured profile paragraph
//   - Stores in Redis as "profile:{cube_id}" with 1-hour TTL
//   - SearchService reads from Redis and injects into search response as profile_mem

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

const (
	profileRefreshTimeout = 60 * time.Second // timeout for background profile refresh
	profileFactsMaxChars  = 4000             // truncate facts to avoid prompt overflow
	profileRespBodyLimit  = 16 * 1024        // 16 KB max LLM response body
)

const (
	profileKeyPrefix = "profile:"
	profileTTL       = time.Hour
	profileMinLength = 50 // minimum chars of UserMemory content needed to generate
)

// profileRefreshCooldown is resolved at startup from MEMDB_PROFILE_REFRESH_COOLDOWN_M (positive int minutes).
// Default 60m (was 10m) — 6× reduction in profiler LLM volume.
var profileRefreshCooldown = envcfg.PositiveDuration("MEMDB_PROFILE_REFRESH_COOLDOWN_M", 60, time.Minute)

// Profiler generates and caches user profile summaries in Redis.
type Profiler struct {
	postgres    *db.Postgres
	redis       *db.Redis
	llmProxyURL string
	llmProxyKey string
	llmModel    string
	logger      *slog.Logger

	mu          sync.Mutex
	lastRefresh map[string]time.Time // per-cube last refresh time (cooldown)
}

// NewProfiler creates a Profiler. All dependencies must be non-nil.
func NewProfiler(pg *db.Postgres, rd *db.Redis, llmURL, llmKey, llmModel string, logger *slog.Logger) *Profiler {
	return &Profiler{
		postgres:    pg,
		redis:       rd,
		llmProxyURL: llmURL,
		llmProxyKey: llmKey,
		llmModel:    llmModel,
		logger:      logger,
	}
}

// TriggerRefresh kicks off a background profile refresh for cubeID.
// Non-blocking: runs in goroutine, failures are logged and silently ignored.
// Cooldown: at most one LLM call per cube per profileRefreshCooldown interval.
func (p *Profiler) TriggerRefresh(cubeID string) {
	p.mu.Lock()
	if p.lastRefresh == nil {
		p.lastRefresh = make(map[string]time.Time)
	}
	if since := time.Since(p.lastRefresh[cubeID]); since < profileRefreshCooldown {
		p.mu.Unlock()
		return // still within cooldown, skip
	}
	p.lastRefresh[cubeID] = time.Now()
	p.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), profileRefreshTimeout)
		defer cancel()
		if err := p.refresh(ctx, cubeID); err != nil {
			p.logger.DebugContext(ctx, "profile refresh failed",
				slog.String("cube_id", cubeID),
				slog.Any("error", err),
			)
		}
	}()
}

// GetProfile reads the cached profile for cubeID from Redis.
// Returns empty string if key is missing or Redis is unavailable.
func (p *Profiler) GetProfile(ctx context.Context, cubeID string) string {
	if p.redis == nil {
		return ""
	}
	val, err := p.redis.Get(ctx, profileKeyPrefix+cubeID)
	if err != nil {
		return ""
	}
	return val
}

// refresh fetches all UserMemory nodes, calls LLM to build profile, caches result.
func (p *Profiler) refresh(ctx context.Context, cubeID string) error {
	// Fetch all UserMemory nodes (up to 100 for profile generation, no vector needed)
	items, _, err := p.postgres.GetAllMemories(ctx, cubeID, "UserMemory", 1, 100)
	if err != nil {
		return fmt.Errorf("profile: fetch user memories: %w", err)
	}

	if len(items) == 0 {
		return nil // nothing to profile yet
	}

	// Build facts string from memory properties
	var facts strings.Builder
	for _, props := range items {
		mem, _ := props["memory"].(string)
		if mem == "" {
			continue
		}
		facts.WriteString("- ")
		facts.WriteString(mem)
		facts.WriteString("\n")
	}

	factsStr := strings.TrimSpace(facts.String())
	if len(factsStr) < profileMinLength {
		return nil // not enough data
	}

	// Call LLM to generate profile
	profile, err := p.callProfileLLM(ctx, factsStr)
	if err != nil {
		return err
	}
	if profile == "" {
		return nil
	}

	// Cache in Redis with 1-hour TTL
	return p.redis.Set(ctx, profileKeyPrefix+cubeID, profile, profileTTL)
}

// callProfileLLM asks the LLM to synthesize a structured user profile paragraph.
//
// Uses llm.ChatText (plain string output, no JSON unmarshal) — the profile is
// a free-form paragraph stored verbatim in Redis. Per-prompt metrics under
// memdb.llm.structured_call_total{prompt_id="profiler"}.
func (p *Profiler) callProfileLLM(ctx context.Context, facts string) (string, error) {
	if len(facts) > profileFactsMaxChars {
		facts = facts[:profileFactsMaxChars] + "\n[truncated]"
	}

	client := llm.NewSimpleClient(p.llmProxyURL, p.llmProxyKey, p.llmModel)
	content, err := llm.ChatText(ctx, client, "profiler", []llm.Message{
		{
			Role:    "system",
			Content: "You are a personal assistant creating a user profile. From the memory facts provided, write a 3-6 sentence profile paragraph in third person covering: name, age, occupation, location, major interests/hobbies, and any other notable stable facts. Be concrete and specific. Do not make things up. If information is unavailable, omit that field.",
		},
		{
			Role:    "user",
			Content: "User memory facts:\n" + facts + "\n\nProfile:",
		},
	},
		llm.WithMaxTokens(400),
		llm.WithTemperature(0.1),
		llm.WithTimeout(30*time.Second),
		llm.WithRespBodyLimit(profileRespBodyLimit),
	)
	if err != nil {
		return "", fmt.Errorf("profile: %w", err)
	}
	return strings.TrimSpace(content), nil
}
