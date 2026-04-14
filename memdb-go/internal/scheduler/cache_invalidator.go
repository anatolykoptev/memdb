package scheduler

// cache_invalidator.go — narrow interface for search-cache invalidation from the Reorganizer.
// Decouples the scheduler package from the handlers package: Reorganizer calls the
// interface; the concrete RedisCacheInvalidator wraps *db.Redis and is wired in server_init.go.

import (
	"context"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// CacheInvalidator invalidates Redis keys that match the given glob patterns.
// Each call should use SCAN + DEL (production-safe, avoids KEYS).
// Implementations must be safe for concurrent use and treat a nil receiver as no-op.
type CacheInvalidator interface {
	Invalidate(ctx context.Context, patterns ...string)
}

// RedisCacheInvalidator is the production implementation of CacheInvalidator.
// It wraps a *redis.Client and performs SCAN+DEL for each pattern.
type RedisCacheInvalidator struct {
	client *redis.Client
	logger *slog.Logger
}

// NewRedisCacheInvalidator creates a RedisCacheInvalidator from a redis.Client.
func NewRedisCacheInvalidator(client *redis.Client, logger *slog.Logger) *RedisCacheInvalidator {
	return &RedisCacheInvalidator{client: client, logger: logger}
}

// Invalidate deletes all Redis keys matching the given patterns via SCAN+DEL.
// Non-fatal: errors are debug-logged, never returned.
func (c *RedisCacheInvalidator) Invalidate(ctx context.Context, patterns ...string) {
	for _, pattern := range patterns {
		var cursor uint64
		for {
			keys, next, err := c.client.Scan(ctx, cursor, pattern, 100).Result()
			if err != nil {
				c.logger.Debug("reorg cache scan error",
					slog.String("pattern", pattern), slog.Any("error", err))
				break
			}
			if len(keys) > 0 {
				if err := c.client.Del(ctx, keys...).Err(); err != nil {
					c.logger.Debug("reorg cache del error", slog.Any("error", err))
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
}
