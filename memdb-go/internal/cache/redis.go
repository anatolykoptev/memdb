// Package cache provides a Redis-backed response cache for the MemDB Go API.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client wraps a Redis client for response caching.
type Client struct {
	rdb    *redis.Client
	logger *slog.Logger
}

// New creates a new cache Client from a Redis URL.
// Returns nil if the URL is empty (cache disabled).
func New(redisURL string, logger *slog.Logger) (*Client, error) {
	if redisURL == "" {
		return nil, nil
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}
	opts.PoolSize = 10
	opts.MinIdleConns = 2

	rdb := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	logger.Info("redis cache connected", slog.String("url", redisURL))
	return &Client{rdb: rdb, logger: logger}, nil
}

// Get retrieves a cached response. Returns nil, nil on cache miss.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return val, nil
}

// Set stores a response with the given TTL.
func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

// Close shuts down the Redis connection.
func (c *Client) Close() error {
	if c.rdb != nil {
		return c.rdb.Close()
	}
	return nil
}

// Ping checks Redis connectivity.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// SearchCacheKey generates a cache key for POST /product/search based on request fields.
func SearchCacheKey(body []byte) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}

	// Key on: user_id, query, top_k, dedup
	parts := fmt.Sprintf("%v:%v:%v:%v",
		m["user_id"], m["query"], m["top_k"], m["dedup"])

	hash := sha256.Sum256([]byte(parts))
	return "memdb:cache:search:" + hex.EncodeToString(hash[:16])
}

// PathCacheKey generates a cache key for GET endpoints.
func PathCacheKey(path string) string {
	return "memdb:cache:path:" + path
}
