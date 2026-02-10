// Package config provides environment-based configuration for the MemDB Go API.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the Go API server.
type Config struct {
	// Server settings
	Port            int           `json:"port"`
	ReadTimeout     time.Duration `json:"read_timeout"`
	WriteTimeout    time.Duration `json:"write_timeout"`
	ShutdownTimeout time.Duration `json:"shutdown_timeout"`

	// Python backend (ConnectRPC / HTTP proxy)
	PythonBackendURL string `json:"python_backend_url"`

	// Authentication
	AuthEnabled       bool   `json:"auth_enabled"`
	MasterKeyHash     string `json:"master_key_hash"`      // SHA-256 hex digest
	InternalServiceSecret string `json:"internal_service_secret"` // service-to-service bypass

	// Logging
	LogLevel  string `json:"log_level"`
	LogFormat string `json:"log_format"` // "json" or "text"

	// OpenTelemetry
	OTelEnabled     bool   `json:"otel_enabled"`
	OTelEndpoint    string `json:"otel_endpoint"`
	OTelServiceName string `json:"otel_service_name"`

	// Caching
	CacheEnabled bool   `json:"cache_enabled"`
	RedisURL     string `json:"redis_url"`

	// Rate limiting
	RateLimitEnabled bool    `json:"rate_limit_enabled"`
	RateLimitRPS     float64 `json:"rate_limit_rps"`
	RateLimitBurst   int     `json:"rate_limit_burst"`

	// Database clients (Phase 2)
	PostgresURL string `json:"postgres_url"`
	QdrantAddr  string `json:"qdrant_addr"` // host:port for gRPC
	DBRedisURL  string `json:"db_redis_url"` // separate from cache Redis DB

	// API settings
	EnableChatAPI bool `json:"enable_chat_api"`
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Port:            envInt("MEMDB_GO_PORT", 8080),
		ReadTimeout:     envDuration("MEMDB_GO_READ_TIMEOUT", 30*time.Second),
		WriteTimeout:    envDuration("MEMDB_GO_WRITE_TIMEOUT", 120*time.Second),
		ShutdownTimeout: envDuration("MEMDB_GO_SHUTDOWN_TIMEOUT", 15*time.Second),

		PythonBackendURL: envStr("MEMDB_PYTHON_URL", "http://localhost:8000"),

		AuthEnabled:       envBool("AUTH_ENABLED", false),
		MasterKeyHash:     envStr("MASTER_KEY_HASH", ""),
		InternalServiceSecret: envStr("INTERNAL_SERVICE_SECRET", ""),

		LogLevel:  envStr("MEMDB_LOG_LEVEL", "info"),
		LogFormat: envStr("MEMDB_LOG_FORMAT", "json"),

		OTelEnabled:     envBool("OTEL_ENABLED", false),
		OTelEndpoint:    envStr("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OTelServiceName: envStr("OTEL_SERVICE_NAME", "memdb-go"),

		CacheEnabled: envBool("MEMDB_CACHE_ENABLED", false),
		RedisURL:     envStr("MEMDB_REDIS_URL", "redis://redis:6379/1"),

		RateLimitEnabled: envBool("MEMDB_RATE_LIMIT_ENABLED", false),
		RateLimitRPS:     envFloat("MEMDB_RATE_LIMIT_RPS", 50),
		RateLimitBurst:   envInt("MEMDB_RATE_LIMIT_BURST", 100),

		PostgresURL: envStr("MEMDB_POSTGRES_URL", ""),
		QdrantAddr:  envStr("MEMDB_QDRANT_ADDR", ""),
		DBRedisURL:  envStr("MEMDB_DB_REDIS_URL", ""),

		EnableChatAPI: envBool("ENABLE_CHAT_API", false),
	}
}

// String returns a human-readable summary of the config.
func (c *Config) String() string {
	return fmt.Sprintf(
		"Config{port=%d, python=%s, log=%s/%s, otel=%v, chat=%v}",
		c.Port, c.PythonBackendURL, c.LogLevel, c.LogFormat,
		c.OTelEnabled, c.EnableChatAPI,
	)
}

// PortStr returns the port as a string.
func (c *Config) PortStr() string {
	return strconv.Itoa(c.Port)
}


// --- helpers ---

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
