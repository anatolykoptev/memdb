package handlers

// handlers_cache.go — Redis cache helpers used by native handlers.
// Covers: cacheGet, cacheSet, cacheDelete, cacheInvalidate.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// cachePrefix is the key namespace for DB-level cache (distinct from middleware's memdb:cache:).
const cachePrefix = "memdb:db:"

// cacheGet reads a value from Redis. Returns nil on miss, error, or if redis is nil.
func (h *Handler) cacheGet(ctx context.Context, key string) []byte {
	if h.redis == nil {
		return nil
	}
	val, err := h.redis.Client().Get(ctx, key).Bytes()
	if err != nil {
		// redis.Nil is a normal cache miss; other errors are debug-logged
		if !errors.Is(err, redis.Nil) {
			h.logger.Debug("cache get error", slog.String("key", key), slog.Any("error", err))
		}
		return nil
	}
	return val
}

// cacheSet stores a value with TTL. No-op if redis is nil. Errors are debug-logged.
func (h *Handler) cacheSet(ctx context.Context, key string, value []byte, ttl time.Duration) {
	if h.redis == nil {
		return
	}
	if err := h.redis.Client().Set(ctx, key, value, ttl).Err(); err != nil {
		h.logger.Debug("cache set error", slog.String("key", key), slog.Any("error", err))
	}
}

// cacheDelete removes a specific key from Redis. No-op if redis is nil. Errors are debug-logged.
func (h *Handler) cacheDelete(ctx context.Context, key string) {
	if h.redis == nil {
		return
	}
	if err := h.redis.Client().Del(ctx, key).Err(); err != nil {
		h.logger.Debug("cache delete error", slog.String("key", key), slog.Any("error", err))
	}
}

// cacheInvalidate deletes keys matching the given patterns. Uses SCAN (production-safe).
// No-op if redis is nil. Errors are debug-logged.
func (h *Handler) cacheInvalidate(ctx context.Context, patterns ...string) {
	if h.redis == nil {
		return
	}
	client := h.redis.Client()
	for _, pattern := range patterns {
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
			if err != nil {
				h.logger.Debug("cache scan error", slog.String("pattern", pattern), slog.Any("error", err))
				break
			}
			if len(keys) > 0 {
				if err := client.Del(ctx, keys...).Err(); err != nil {
					h.logger.Debug("cache del error", slog.Any("error", err))
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
}
