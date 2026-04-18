// Package config provides environment-based configuration for the MemDB Go API.
package config

import (
	"fmt"
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
	AuthEnabled           bool   `json:"auth_enabled"`
	MasterKeyHash         string `json:"master_key_hash"`         // SHA-256 hex digest
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
	QdrantAddr  string `json:"qdrant_addr"`  // host:port for gRPC
	DBRedisURL  string `json:"db_redis_url"` // separate from cache Redis DB

	// Embedder
	EmbedderType     string `json:"embedder_type"`       // "onnx", "voyage", or "ollama"
	ONNXModelDir     string `json:"onnx_model_dir"`      // path to ONNX model files
	ONNXModelDirCode string `json:"onnx_model_dir_code"` // path to code ONNX model (optional)
	VoyageAPIKey     string `json:"voyage_api_key"`      // kept for rollback
	EmbedderModel    string `json:"embedder_model"`      // model name (voyage or ollama)
	OllamaURL        string `json:"ollama_url"`          // Ollama server URL (e.g. http://ollama:11434)
	OllamaDim        int    `json:"ollama_dim"`          // embedding dimension override (0 = use model default)
	OllamaPrefix     string `json:"ollama_prefix"`       // client-side text prefix ("" = no prefix, raw text like ONNX)
	OllamaQuery      string `json:"ollama_query"`        // client-side query prefix for EmbedQuery ("" = same as OllamaPrefix)
	EmbedURL         string `json:"embed_url"`           // HTTP embed-server sidecar URL (for type="http")
	EmbedURLCode     string `json:"embed_url_code"`      // separate sidecar URL for code model (optional)

	// API settings
	EnableChatAPI bool `json:"enable_chat_api"`

	// LLM proxy (OpenAI-compatible API base URL)
	LLMProxyURL       string   `json:"llm_proxy_url"`
	LLMProxyAPIKey    string   `json:"llm_proxy_api_key"`
	LLMDefaultModel   string   `json:"llm_default_model"`
	LLMSearchModel    string   `json:"llm_search_model"`    // model for search LLM calls: rerank, iterative (default: gemini-2.0-flash)
	LLMExtractModel   string   `json:"llm_extract_model"`   // model for fine-mode extraction (default: gemini-2.0-flash-lite)
	LLMReorgModel     string   `json:"llm_reorg_model"`     // model for memory reorganizer consolidation (default: gemini-2.5-flash-lite)
	LLMFallbackModels []string `json:"llm_fallback_models"` // fallback models tried on quota errors (comma-separated env)
	ReorgUseHNSW      bool     `json:"reorg_use_hnsw"`      // use HNSW index for FindNearDuplicates (env: MEMDB_REORG_USE_HNSW, default false)

	// Buffer zone (batch add before LLM extraction)
	BufferEnabled bool          `json:"buffer_enabled"`
	BufferSize    int           `json:"buffer_size"`
	BufferTTL     time.Duration `json:"buffer_ttl"`

	// Ingestion queue (bounded concurrency for /product/add)
	AddWorkers   int `json:"add_workers"`    // max concurrent native add requests
	AddQueueSize int `json:"add_queue_size"` // max requests waiting in queue

	// SearXNG URL for internet search
	SearXNGURL string `json:"searxng_url"`

	// Webshare proxy API key (enables DDG/Startpage direct scrapers)
	WebshareAPIKey string `json:"webshare_api_key"`

	// MemDB Go API URL (used by MCP server to proxy search)
	MemDBGoURL string `json:"memdb_go_url"`
}

const (
	defaultPort            = 8080
	defaultReadTimeout     = 30 * time.Second
	defaultWriteTimeout    = 120 * time.Second
	defaultShutdownTimeout = 15 * time.Second
	defaultRateLimitRPS    = 50
	defaultBufferSize      = 5
	defaultBufferTTL       = 30 * time.Second
	defaultAddWorkers      = 4
	defaultAddQueueSize    = 50
)

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Port:            envInt("MEMDB_GO_PORT", defaultPort),
		ReadTimeout:     envDuration("MEMDB_GO_READ_TIMEOUT", defaultReadTimeout),
		WriteTimeout:    envDuration("MEMDB_GO_WRITE_TIMEOUT", defaultWriteTimeout),
		ShutdownTimeout: envDuration("MEMDB_GO_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),

		PythonBackendURL: envStr("MEMDB_PYTHON_URL", "http://localhost:8000"),

		AuthEnabled:           envBool("AUTH_ENABLED", false),
		MasterKeyHash:         envStr("MASTER_KEY_HASH", ""),
		InternalServiceSecret: envStr("INTERNAL_SERVICE_SECRET", ""),

		LogLevel:  envStr("MEMDB_LOG_LEVEL", "info"),
		LogFormat: envStr("MEMDB_LOG_FORMAT", "json"),

		OTelEnabled:     envBool("OTEL_ENABLED", false),
		OTelEndpoint:    envStr("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OTelServiceName: envStr("OTEL_SERVICE_NAME", "memdb-go"),

		CacheEnabled: envBool("MEMDB_CACHE_ENABLED", false),
		RedisURL:     envStr("MEMDB_REDIS_URL", "redis://redis:6379/1"),

		RateLimitEnabled: envBool("MEMDB_RATE_LIMIT_ENABLED", false),
		RateLimitRPS:     envFloat("MEMDB_RATE_LIMIT_RPS", defaultRateLimitRPS),
		RateLimitBurst:   envInt("MEMDB_RATE_LIMIT_BURST", 100),

		PostgresURL: envStr("MEMDB_POSTGRES_URL", ""),
		QdrantAddr:  envStr("MEMDB_QDRANT_ADDR", ""),
		DBRedisURL:  envStr("MEMDB_DB_REDIS_URL", ""),

		EmbedderType:     envStr("MEMDB_EMBEDDER_TYPE", "onnx"),
		ONNXModelDir:     envStr("MEMDB_ONNX_MODEL_DIR", "/models"),
		ONNXModelDirCode: envStr("MEMDB_ONNX_MODEL_DIR_CODE", ""),
		VoyageAPIKey:     envStr("VOYAGE_API_KEY", ""),
		EmbedderModel:    envStr("MEMDB_EMBEDDER_MODEL", "voyage-4-lite"),
		OllamaURL:        envStr("MEMDB_OLLAMA_URL", "http://localhost:11434"),
		OllamaDim:        envInt("MEMDB_OLLAMA_DIM", 0),
		OllamaPrefix:     envStr("MEMDB_OLLAMA_PREFIX", ""),
		OllamaQuery:      envStr("MEMDB_OLLAMA_QUERY_PREFIX", ""),
		EmbedURL:         envStr("MEMDB_EMBED_URL", ""),
		EmbedURLCode:     envStr("MEMDB_EMBED_URL_CODE", ""),

		EnableChatAPI: envBool("ENABLE_CHAT_API", false),

		LLMProxyURL:       envStr("MEMDB_LLM_PROXY_URL", "https://api.openai.com/v1"),
		LLMProxyAPIKey:    envStr("CLI_PROXY_API_KEY", ""),
		LLMDefaultModel:   envStr("MEMDB_LLM_MODEL", "gemini-2.5-flash"),
		LLMSearchModel:    envStr("MEMDB_LLM_SEARCH_MODEL", "gemini-2.0-flash"),
		LLMExtractModel:   envStr("MEMDB_LLM_EXTRACT_MODEL", "gemini-2.0-flash-lite"),
		LLMReorgModel:     envStr("MEMDB_REORG_LLM_MODEL", "gemini-2.5-flash-lite"),
		LLMFallbackModels: envCSV("MEMDB_LLM_FALLBACK_MODELS", nil),
		ReorgUseHNSW:      envBool("MEMDB_REORG_USE_HNSW", false),

		BufferEnabled: envBool("MEMDB_BUFFER_ENABLED", false),
		BufferSize:    envInt("MEMDB_BUFFER_SIZE", defaultBufferSize),
		BufferTTL:     envDuration("MEMDB_BUFFER_TTL", defaultBufferTTL),

		AddWorkers:   envInt("MEMDB_ADD_WORKERS", defaultAddWorkers),
		AddQueueSize: envInt("MEMDB_ADD_QUEUE_SIZE", defaultAddQueueSize),

		SearXNGURL:     envStr("SEARXNG_URL", ""),
		WebshareAPIKey: envStr("WEBSHARE_API_KEY", ""),
		MemDBGoURL:     envStr("MEMDB_GO_URL", ""),
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
