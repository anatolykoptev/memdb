package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
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
	opts.PoolSize = envIntOrDefault("MEMDB_REDIS_POOL_SIZE", 10, 2, 100)
	opts.MinIdleConns = envIntOrDefault("MEMDB_REDIS_MIN_IDLE", 2, 0, 50)
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

	logger.InfoContext(ctx, "redis connected", slog.String("url", redisURL))
	return &Redis{client: client, logger: logger}, nil
}

// NewRedisFromClient wraps an existing Redis client (useful for tests with miniredis).
func NewRedisFromClient(client *redis.Client, logger *slog.Logger) *Redis {
	return &Redis{client: client, logger: logger}
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

// redisPoolMetrics records Redis connection pool stats as Prometheus gauges.
var (
	redisPoolOnce  sync.Once
	poolTotalConns metric.Int64ObservableGauge
	poolIdleConns  metric.Int64ObservableGauge
	poolStaleConns metric.Int64ObservableGauge
)

// RegisterRedisPoolMetrics wires go-redis PoolStats to Prometheus gauges.
// Call once at startup after the Redis client is created.
func RegisterRedisPoolMetrics(r *Redis) {
	redisPoolOnce.Do(func() {
		meter := otel.Meter("memdb-go/redis")
		poolTotalConns, _ = meter.Int64ObservableGauge("memdb.redis.pool_total_conns",
			metric.WithDescription("Total connections in the Redis pool (active+idle)"),
		)
		poolIdleConns, _ = meter.Int64ObservableGauge("memdb.redis.pool_idle_conns",
			metric.WithDescription("Idle connections in the Redis pool"),
		)
		poolStaleConns, _ = meter.Int64ObservableGauge("memdb.redis.pool_stale_conns",
			metric.WithDescription("Stale connections removed from the Redis pool"),
		)
		meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
			stats := r.client.PoolStats()
			o.ObserveInt64(poolTotalConns, int64(stats.TotalConns))
			o.ObserveInt64(poolIdleConns, int64(stats.IdleConns))
			o.ObserveInt64(poolStaleConns, int64(stats.StaleConns))
			return nil
		}, poolTotalConns, poolIdleConns, poolStaleConns)
	})
}

// Get retrieves a string value by key. Returns error if key is missing (redis.Nil).
func (r *Redis) Get(ctx context.Context, key string) (string, error) {
	return r.client.Get(ctx, key).Result()
}

// Set stores a string value with an optional TTL (0 = no expiry).
func (r *Redis) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return r.client.Set(ctx, key, value, ttl).Err()
}
