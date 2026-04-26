// Package cache provides a Redis-backed response cache for the MemDB Go API.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const redisPingTimeout = 3 * time.Second

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

	ctx, cancel := context.WithTimeout(context.Background(), redisPingTimeout)
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
	if errors.Is(err, redis.Nil) {
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

// searchKeyFields holds all fields that affect the /product/search response.
// Extending this struct (rather than changing the function signature) is
// the safe way to add new dimensions — callers are compile-time checked.
type searchKeyFields struct {
	UserID    string // user_id from request
	Query     string // raw query string
	TopK      any    // top_k (int or nil → 0)
	Dedup     string // dedup value
	Level     string // level tier ("", "l1", "l2", "l3")
	AgentID   string // agent_id ("" = cross-agent)
	PrefTopK  int    // pref_top_k (0 = default)
}

// SearchCacheKey generates a cache key for POST /product/search.
// All seven fields that affect the response payload are included so that
// requests differing only in level / agent_id / pref_top_k do not collide.
//
// Key version: v2 (bumped from v1 which omitted level/agent_id/pref_top_k).
// Old v1 entries in Redis will simply miss and be repopulated — no migration
// needed because the cache is non-authoritative (source-of-truth is Postgres).
func SearchCacheKey(f searchKeyFields) string {
	// Canonical pipe-separated concatenation; pipes are not valid in any field.
	parts := fmt.Sprintf("%s|%s|%v|%s|%s|%s|%d",
		f.UserID, f.Query, f.TopK, f.Dedup, f.Level, f.AgentID, f.PrefTopK)
	hash := sha256.Sum256([]byte(parts))
	return "memdb:cache:search:v2:" + hex.EncodeToString(hash[:16])
}

// ParseSearchCacheKey extracts searchKeyFields from a raw JSON request body
// so the middleware can build the cache key before passing control to the handler.
// Returns zero-value fields (empty strings / 0) for absent JSON keys.
func ParseSearchCacheKey(body []byte) (searchKeyFields, error) {
	var m struct {
		UserID   string `json:"user_id"`
		Query    string `json:"query"`
		TopK     *int   `json:"top_k"`
		Dedup    string `json:"dedup"`
		Level    string `json:"level"`
		AgentID  string `json:"agent_id"`
		PrefTopK int    `json:"pref_top_k"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return searchKeyFields{}, err
	}
	topK := 0
	if m.TopK != nil {
		topK = *m.TopK
	}
	return searchKeyFields{
		UserID:   m.UserID,
		Query:    m.Query,
		TopK:     topK,
		Dedup:    m.Dedup,
		Level:    m.Level,
		AgentID:  m.AgentID,
		PrefTopK: m.PrefTopK,
	}, nil
}

// PathCacheKey generates a cache key for GET endpoints.
func PathCacheKey(path string) string {
	return "memdb:cache:path:" + path
}
