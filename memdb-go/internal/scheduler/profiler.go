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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	profileRefreshTimeout  = 60 * time.Second // timeout for background profile refresh
	profileFactsMaxChars   = 4000             // truncate facts to avoid prompt overflow
	profileRespBodyLimit   = 16 * 1024        // 16 KB max LLM response body
)

const (
	profileKeyPrefix = "profile:"
	profileTTL       = time.Hour
	profileMinLength = 50 // minimum chars of UserMemory content needed to generate
)

// Profiler generates and caches user profile summaries in Redis.
type Profiler struct {
	postgres      *db.Postgres
	redis         *db.Redis
	llmProxyURL   string
	llmProxyKey   string
	llmModel      string
	logger        *slog.Logger
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
func (p *Profiler) TriggerRefresh(cubeID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), profileRefreshTimeout)
		defer cancel()
		if err := p.refresh(ctx, cubeID); err != nil {
			p.logger.Debug("profile refresh failed",
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
func (p *Profiler) callProfileLLM(ctx context.Context, facts string) (string, error) {
	if len(facts) > profileFactsMaxChars {
		facts = facts[:profileFactsMaxChars] + "\n[truncated]"
	}

	payload := map[string]any{
		"model":       p.llmModel,
		"temperature": 0.1,
		"max_tokens":  400,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a personal assistant creating a user profile. From the memory facts provided, write a 3-6 sentence profile paragraph in third person covering: name, age, occupation, location, major interests/hobbies, and any other notable stable facts. Be concrete and specific. Do not make things up. If information is unavailable, omit that field.",
			},
			{
				"role":    "user",
				"content": "User memory facts:\n" + facts + "\n\nProfile:",
			},
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.llmProxyURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.llmProxyKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.llmProxyKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, profileRespBodyLimit))
	if err != nil {
		return "", err
	}

	var chatResp struct {
		Choices []struct {
			Message struct{ Content string `json:"content"` } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return "", errors.New("profile: bad LLM response")
	}

	return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
}
