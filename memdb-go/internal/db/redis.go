package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const redisPingTimeout = 3 * time.Second

// Redis wraps a go-redis client for shared use (cache + general storage).
type Redis struct {
	client *redis.Client
	logger *slog.Logger
}

// NewRedis creates a new Redis client from a URL.
func NewRedis(ctx context.Context, redisURL string, logger *slog.Logger) (*Redis, error) {
	if redisURL == "" {
		return nil, errors.New("redis URL is empty")
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}
	opts.PoolSize = 10
	opts.MinIdleConns = 2
	// UnstableResp3 is required for Redis 8 VSET commands (VADD, VSIM, etc.)
	// go-redis v9 marks VSET API as experimental and requires this flag.
	opts.UnstableResp3 = true

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, redisPingTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	logger.Info("redis connected", slog.String("url", redisURL))
	return &Redis{client: client, logger: logger}, nil
}

// Client returns the underlying Redis client.
func (r *Redis) Client() *redis.Client {
	return r.client
}

// Ping checks the Redis connection.
func (r *Redis) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Close closes the Redis connection.
func (r *Redis) Close() error {
	return r.client.Close()
}

// Get retrieves a string value by key. Returns error if key is missing (redis.Nil).
func (r *Redis) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, key).Result()
}

// Set stores a string value with an optional TTL (0 = no expiry).
func (r *Redis) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}
