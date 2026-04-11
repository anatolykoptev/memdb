package handlers

// add_buffer.go — buffer zone for batching add requests before LLM extraction.
//
// When enabled, messages with no explicit mode are accumulated in Redis and
// flushed through the fine pipeline once a count or time threshold is reached.
// This reduces LLM calls by up to 80% for rapid-fire add sequences.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	bufferKeyPrefix     = "memdb:buf:"
	bufferSafetyTTL     = 5 * time.Minute
	bufferFlushInterval = 10 * time.Second
)

// bufferEntry is a single buffered conversation.
type bufferEntry struct {
	Conversation string `json:"c"`
	Timestamp    int64  `json:"t"`
}

// bufferKey returns the Redis key for a cube's buffer.
func bufferKey(cubeID string) string {
	return bufferKeyPrefix + cubeID
}

// bufferAddForCube pushes a formatted conversation into the Redis buffer.
// If the buffer reaches the count threshold, it flushes immediately and
// returns the extraction results. Otherwise, returns a "buffered" response.
func (h *Handler) bufferAddForCube(ctx context.Context, req *fullAddRequest, cubeID string) ([]addResponseItem, error) {
	if len(req.Messages) == 0 {
		return nil, nil
	}

	now := nowTimestamp()
	conversation := formatConversation(req.Messages, now)

	entry := bufferEntry{
		Conversation: conversation,
		Timestamp:    time.Now().Unix(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("buffer add: marshal entry: %w", err)
	}

	client := h.redis.Client()
	key := bufferKey(cubeID)

	// Push entry and reset safety TTL (sliding window)
	pipe := client.Pipeline()
	pipe.RPush(ctx, key, data)
	pipe.Expire(ctx, key, bufferSafetyTTL)
	results, err := pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("buffer add: redis push: %w", err)
	}

	// First result is RPush — its value is the new list length
	bufLen := results[0].(*redis.IntCmd).Val()

	h.logger.Debug("buffer add: pushed",
		slog.String("cube_id", cubeID),
		slog.Int64("buf_len", bufLen),
	)

	// Check count threshold
	if int(bufLen) >= h.bufferCfg.Size {
		return h.flushBuffer(ctx, cubeID)
	}

	// Not yet at threshold — return buffered response
	return []addResponseItem{{
		Memory:     "buffered for batch processing",
		MemoryType: "buffer",
		MemoryID:   "",
		CubeID:     cubeID,
	}}, nil
}

// flushBuffer atomically reads and deletes all entries from a cube's buffer,
// concatenates them, and runs the full fine extraction pipeline.
// On pipeline failure, entries are re-pushed to Redis so they aren't lost.
func (h *Handler) flushBuffer(ctx context.Context, cubeID string) ([]addResponseItem, error) {
	client := h.redis.Client()
	key := bufferKey(cubeID)

	// Atomic read + delete via Lua script
	luaScript := redis.NewScript(`
local msgs = redis.call('LRANGE', KEYS[1], 0, -1)
if #msgs > 0 then
    redis.call('DEL', KEYS[1])
end
return msgs
`)
	result, err := luaScript.Run(ctx, client, []string{key}).StringSlice()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("buffer flush: lua script: %w", err)
	}
	if len(result) == 0 {
		return nil, nil
	}

	// Decode entries and concatenate conversations
	conversations := make([]string, 0, len(result))
	for _, raw := range result {
		var entry bufferEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			h.logger.Debug("buffer flush: skip malformed entry", slog.Any("error", err))
			continue
		}
		conversations = append(conversations, entry.Conversation)
	}
	if len(conversations) == 0 {
		return nil, nil
	}

	conversation := strings.Join(conversations, "\n---\n")

	h.logger.Info("buffer flush: processing batch",
		slog.String("cube_id", cubeID),
		slog.Int("entries", len(conversations)),
		slog.Int("chars", len(conversation)),
	)

	// Run the same fine pipeline as nativeFineAddForCube, but with the
	// concatenated conversation text instead of a single request's messages.
	items, err := h.runFinePipeline(ctx, conversation, cubeID)
	if err != nil {
		// Pipeline failed — re-push entries so they aren't lost.
		// The background flusher will retry on the next tick.
		h.repushEntries(ctx, key, result)
		return nil, err
	}
	return items, nil
}

// repushEntries pushes raw entry strings back to Redis after a failed flush.
// Non-fatal: if re-push fails, entries are logged and lost (safety TTL would
// have expired them eventually anyway).
func (h *Handler) repushEntries(ctx context.Context, key string, entries []string) {
	client := h.redis.Client()
	pipe := client.Pipeline()
	vals := make([]interface{}, len(entries))
	for i, e := range entries {
		vals[i] = e
	}
	pipe.RPush(ctx, key, vals...)
	pipe.Expire(ctx, key, bufferSafetyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		h.logger.Error("buffer flush: failed to re-push entries after pipeline error",
			slog.String("key", key),
			slog.Int("entries", len(entries)),
			slog.Any("error", err),
		)
	} else {
		h.logger.Warn("buffer flush: re-pushed entries for retry",
			slog.String("key", key),
			slog.Int("entries", len(entries)),
		)
	}
}

// runFinePipeline executes the fine extraction pipeline on a pre-formatted conversation string.
// This is the core of nativeFineAddForCube, extracted so both sync fine and buffer flush can use it.
func (h *Handler) runFinePipeline(ctx context.Context, conversation, cubeID string) ([]addResponseItem, error) {
	now := nowTimestamp()

	// Content router (text-only variant for buffer zone)
	sig := classifyContentFromText(conversation)

	// Step 1: candidate fetch for dedup context
	candidates, topScore := h.fetchFineCandidates(ctx, conversation, cubeID, "")
	if topScore > nearDuplicateThreshold {
		h.logger.Debug("buffer flush: skipped — near-duplicate",
			slog.Float64("top_score", topScore), slog.String("cube_id", cubeID))
		return nil, nil
	}

	// Merge hint for high-similarity content
	if topScore > mergeSuggestionThreshold {
		sig.Hints = append(sig.Hints, "High-similarity existing memory found — prefer UPDATE over ADD if semantically equivalent")
	}

	// Step 2: unified LLM extraction + dedup (with content hints)
	facts, err := h.llmExtractor.ExtractAndDedup(ctx, conversation, candidates, sig.Hints...)
	if err != nil {
		return nil, fmt.Errorf("buffer flush: extract and dedup: %w", err)
	}
	if len(facts) == 0 {
		h.logger.Debug("buffer flush: no facts extracted", slog.String("cube_id", cubeID))
		return nil, nil
	}
	h.logger.Debug("buffer flush: extracted facts",
		slog.Int("count", len(facts)),
		slog.String("model", h.llmExtractor.Model()),
	)

	// Step 3: filter exact duplicates by content-hash
	facts = h.filterAddsByContentHash(ctx, facts, cubeID)

	// Step 4: parallel embed
	embedded := h.embedFacts(ctx, facts)

	// Step 5: apply actions (no session/agent context for buffered adds)
	// Buffer flush has no original request — use cubeID as userID fallback (no person identity available).
	allNodes, items, vsetInserts := h.applyFineActions(ctx, embedded, cubeID, cubeID, "", "", now, nil, nil, nil)

	if len(allNodes) > 0 {
		if err := h.postgres.InsertMemoryNodes(ctx, allNodes); err != nil {
			return nil, fmt.Errorf("buffer flush: insert nodes: %w", err)
		}
		if h.wmCache != nil {
			ts := nowUnix()
			for _, vi := range vsetInserts {
				if err := h.wmCache.VAdd(ctx, cubeID, vi.id, vi.memory, vi.embedding, ts); err != nil {
					h.logger.Debug("buffer flush: vset write failed",
						slog.String("id", vi.id), slog.Any("error", err))
				}
			}
		}
		h.linkEntitiesAsync(embedded, cubeID, now)
	}

	// Cleanup
	h.cleanupWorkingMemory(ctx, cubeID)

	// Episodic summary for the entire batch
	// Buffer flush has no original request — use cubeID as userID fallback.
	h.generateEpisodicSummary(cubeID, cubeID, "", conversation, now, len(facts))

	// Profile refresh
	if h.profiler != nil {
		h.profiler.TriggerRefresh(cubeID)
	}

	return items, nil
}

// StartBufferFlusher runs a background goroutine that periodically checks for
// stale buffers (oldest entry age > TTL) and flushes them.
func (h *Handler) StartBufferFlusher(ctx context.Context) {
	ticker := time.NewTicker(bufferFlushInterval)
	defer ticker.Stop()

	h.logger.Info("buffer flusher started",
		slog.Duration("interval", bufferFlushInterval),
		slog.Duration("ttl", h.bufferCfg.TTL),
	)

	for {
		select {
		case <-ctx.Done():
			h.logger.Info("buffer flusher stopped")
			return
		case <-ticker.C:
			h.checkStaleBuffers(ctx)
		}
	}
}

// checkStaleBuffers scans for buffer keys with entries older than the TTL and flushes them.
func (h *Handler) checkStaleBuffers(ctx context.Context) {
	if h.redis == nil {
		return
	}
	client := h.redis.Client()

	// SCAN for buffer keys
	var cursor uint64
	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, bufferKeyPrefix+"*", 100).Result()
		if err != nil {
			h.logger.Debug("buffer flusher: scan failed", slog.Any("error", err))
			return
		}

		for _, key := range keys {
			h.checkAndFlushStale(ctx, client, key)
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
}

// checkAndFlushStale checks if the oldest entry in a buffer is older than TTL, and flushes if so.
func (h *Handler) checkAndFlushStale(ctx context.Context, client *redis.Client, key string) {
	// Peek at the oldest entry (index 0)
	vals, err := client.LRange(ctx, key, 0, 0).Result()
	if err != nil || len(vals) == 0 {
		return
	}

	var entry bufferEntry
	if err := json.Unmarshal([]byte(vals[0]), &entry); err != nil {
		return
	}

	age := time.Since(time.Unix(entry.Timestamp, 0))
	if age < h.bufferCfg.TTL {
		return
	}

	// Extract cubeID from key
	cubeID := strings.TrimPrefix(key, bufferKeyPrefix)
	h.logger.Debug("buffer flusher: stale buffer detected",
		slog.String("cube_id", cubeID),
		slog.Duration("age", age),
	)

	items, err := h.flushBuffer(ctx, cubeID)
	if err != nil {
		h.logger.Error("buffer flusher: flush failed",
			slog.String("cube_id", cubeID),
			slog.Any("error", err),
		)
		return
	}

	// Invalidate caches after background flush
	if len(items) > 0 {
		h.cacheInvalidate(ctx,
			cachePrefix+"get_all:"+cubeID+":*",
			cachePrefix+"post_get_memory:"+cubeID+":*",
		)
		h.logger.Info("buffer flusher: flushed stale buffer",
			slog.String("cube_id", cubeID),
			slog.Int("memories", len(items)),
		)
	}
}
