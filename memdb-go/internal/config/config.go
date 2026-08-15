// Package config provides environment-based configuration for the MemDB Go API.
package config

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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

	// DevMode (env: MEMDB_DEV=1) relaxes startup safety checks for local
	// development. Production deployments must NOT set this — Validate()
	// refuses to start without auth config when DevMode is false.
	DevMode bool `json:"dev_mode"`

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

	// Chat defaults — applied when the per-request field is absent.
	// Valid values: "" (no override), "conversational", "factual".
	// Invalid values fall back to "conversational" with a log warning.
	DefaultAnswerStyle string `json:"default_answer_style"`

	// FactualCanaryPct is the percentage of users routed to the factual answer-style canary.
	// Range [0, 100]. 0 = canary off (default). Out-of-range values are clamped with a log warning.
	// Env: MEMDB_FACTUAL_CANARY_PCT.
	FactualCanaryPct int `json:"factual_canary_pct"`

	// LLM proxy (OpenAI-compatible API base URL)
	LLMProxyURL       string   `json:"llm_proxy_url"`
	LLMProxyAPIKey    string   `json:"llm_proxy_api_key"`
	LLMDefaultModel   string   `json:"llm_default_model"`
	LLMSearchModel    string   `json:"llm_search_model"`    // model for search LLM calls: rerank, iterative (default: gemini-2.0-flash)
	LLMExtractModel   string   `json:"llm_extract_model"`   // model for fine-mode extraction (default: gemini-2.0-flash-lite)
	LLMBufferModel    string   `json:"llm_buffer_model"`    // model for buffer-mode extraction; empty = fall back to LLMExtractModel (env: MEMDB_BUFFER_LLM_MODEL)
	LLMReorgModel     string   `json:"llm_reorg_model"`     // model for memory reorganizer consolidation (default: gemini-2.5-flash-lite)
	LLMFallbackModels []string `json:"llm_fallback_models"` // fallback models tried on quota errors (comma-separated env)
	ReorgUseHNSW      bool     `json:"reorg_use_hnsw"`      // use HNSW index for FindNearDuplicates (env: MEMDB_REORG_USE_HNSW, default false) — deprecated, prefer ReorgDupStrategy
	// ReorgDupStrategy selects the near-duplicate detection path: "auto",
	// "legacy", or "hnsw". Env: MEMDB_REORG_DUP_STRATEGY. When set it wins
	// over the deprecated MEMDB_REORG_USE_HNSW boolean. Empty + USE_HNSW=true
	// → "hnsw"; empty + USE_HNSW=false → "auto".
	ReorgDupStrategy string `json:"reorg_dup_strategy"`
	// ReorgDupCrossover is the memory count at which the auto router switches
	// from legacy to HNSW. Env: MEMDB_REORG_DUP_CROSSOVER, default 1000.
	ReorgDupCrossover int `json:"reorg_dup_crossover"`

	// ReorgVsetPerCubeCap is the maximum number of WorkingMemory nodes kept
	// in the Redis VSET hot cache per cube. When VCard exceeds this cap the
	// VSetEvictionPolicy evicts oldest-first entries so dedup candidates stay
	// fresh. A per-cube cap is required because Redis allkeys-lru is global —
	// one hot cube would starve cold cubes without it.
	// Env: MEMDB_VSET_PER_CUBE_CAP, default 500.
	ReorgVsetPerCubeCap int `json:"reorg_vset_per_cube_cap"`

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

	// Cross-encoder reranker (embed-server /v1/rerank, gte-multi-rerank).
	// Empty URL disables the step. No SearchParams flag: controlled entirely
	// by env so every handler, MCP, and test gets consistent behavior.
	CrossEncoderURL            string        `json:"cross_encoder_url"`
	CrossEncoderModel          string        `json:"cross_encoder_model"`
	CrossEncoderAPIKey         string        `json:"cross_encoder_api_key"`
	CrossEncoderTimeout        time.Duration `json:"cross_encoder_timeout"`
	CrossEncoderMaxDocs        int           `json:"cross_encoder_max_docs"`
	CrossEncoderMaxCharsPerDoc int           `json:"cross_encoder_max_chars_per_doc"`

	// D11 CoT decomposer (multi-hop / temporal query decomposition).
	// Default OFF for zero regression. Sibling of MEMDB_SEARCH_COT (D7) —
	// they can be enabled independently for ablation.
	CoTDecompose     bool `json:"cot_decompose"`      // env: MEMDB_COT_DECOMPOSE
	CoTMaxSubqueries int  `json:"cot_max_subqueries"` // env: MEMDB_COT_MAX_SUBQUERIES, default 3, clamp [1,5]
	CoTTimeoutMS     int  `json:"cot_timeout_ms"`     // env: MEMDB_COT_TIMEOUT_MS, default 2000, clamp [500,10000]
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

	// Cross-encoder reranker defaults.
	defaultCrossEncoderURL            = "http://embed-server:8082"
	defaultCrossEncoderModel          = "gte-multi-rerank"
	defaultCrossEncoderTimeoutMS      = 2000
	defaultCrossEncoderMaxDocs        = 50
	defaultCrossEncoderMaxCharsPerDoc = 0 // 0 = no truncation; recommend 200 for ARM CPU rerankers

	// D11 CoT decomposer defaults.
	defaultCoTMaxSubqueries = 3
	defaultCoTTimeoutMS     = 2000
	cotMaxSubqueriesMin     = 1
	cotMaxSubqueriesMax     = 5
	cotTimeoutMSMin         = 500
	cotTimeoutMSMax         = 10000

	// Reorganizer dup-strategy defaults.
	defaultReorgDupCrossover = 1000

	// VSET per-cube eviction cap default.
	defaultReorgVsetPerCubeCap = 500
)

// clampCoTMaxSubqueries enforces [cotMaxSubqueriesMin, cotMaxSubqueriesMax].
func clampCoTMaxSubqueries(v int) int {
	if v < cotMaxSubqueriesMin {
		return cotMaxSubqueriesMin
	}
	if v > cotMaxSubqueriesMax {
		return cotMaxSubqueriesMax
	}
	return v
}

// clampCoTTimeoutMS enforces [cotTimeoutMSMin, cotTimeoutMSMax].
func clampCoTTimeoutMS(v int) int {
	if v < cotTimeoutMSMin {
		return cotTimeoutMSMin
	}
	if v > cotTimeoutMSMax {
		return cotTimeoutMSMax
	}
	return v
}

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
		DevMode:               envBool("MEMDB_DEV", false),

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

		EnableChatAPI:      envBool("ENABLE_CHAT_API", false),
		DefaultAnswerStyle: validatedAnswerStyle(envStr("MEMDB_DEFAULT_ANSWER_STYLE", "")),
		FactualCanaryPct:   clampCanaryPct(envInt("MEMDB_FACTUAL_CANARY_PCT", 0)),

		LLMProxyURL:         envStr("MEMDB_LLM_PROXY_URL", "https://api.openai.com/v1"),
		LLMProxyAPIKey:      envStr("CLI_PROXY_API_KEY", ""),
		LLMDefaultModel:     envStr("MEMDB_LLM_MODEL", "gemini-2.5-flash"),
		LLMSearchModel:      envStr("MEMDB_LLM_SEARCH_MODEL", "gemini-2.0-flash"),
		LLMExtractModel:     envStr("MEMDB_LLM_EXTRACT_MODEL", "gemini-2.0-flash-lite"),
		LLMBufferModel:      envStr("MEMDB_BUFFER_LLM_MODEL", ""),
		LLMReorgModel:       envStr("MEMDB_REORG_LLM_MODEL", "gemini-2.5-flash-lite"),
		LLMFallbackModels:   envCSV("MEMDB_LLM_FALLBACK_MODELS", nil),
		ReorgUseHNSW:        envBool("MEMDB_REORG_USE_HNSW", false),
		ReorgDupStrategy:    envStr("MEMDB_REORG_DUP_STRATEGY", ""),
		ReorgDupCrossover:   envInt("MEMDB_REORG_DUP_CROSSOVER", defaultReorgDupCrossover),
		ReorgVsetPerCubeCap: envInt("MEMDB_VSET_PER_CUBE_CAP", defaultReorgVsetPerCubeCap),

		BufferEnabled: envBool("MEMDB_BUFFER_ENABLED", false),
		BufferSize:    envInt("MEMDB_BUFFER_SIZE", defaultBufferSize),
		BufferTTL:     envDuration("MEMDB_BUFFER_TTL", defaultBufferTTL),

		AddWorkers:   envInt("MEMDB_ADD_WORKERS", defaultAddWorkers),
		AddQueueSize: envInt("MEMDB_ADD_QUEUE_SIZE", defaultAddQueueSize),

		SearXNGURL:     envStr("SEARXNG_URL", ""),
		WebshareAPIKey: envStr("WEBSHARE_API_KEY", ""),
		MemDBGoURL:     envStr("MEMDB_GO_URL", ""),

		CrossEncoderURL:            envStr("CROSS_ENCODER_URL", defaultCrossEncoderURL),
		CrossEncoderModel:          envStr("CROSS_ENCODER_MODEL", defaultCrossEncoderModel),
		CrossEncoderAPIKey:         envStr("CROSS_ENCODER_API_KEY", ""),
		CrossEncoderTimeout:        time.Duration(envInt("CROSS_ENCODER_TIMEOUT_MS", defaultCrossEncoderTimeoutMS)) * time.Millisecond,
		CrossEncoderMaxDocs:        envInt("CROSS_ENCODER_MAX_DOCS", defaultCrossEncoderMaxDocs),
		CrossEncoderMaxCharsPerDoc: envInt("CROSS_ENCODER_MAX_CHARS_PER_DOC", defaultCrossEncoderMaxCharsPerDoc),

		CoTDecompose:     envBool("MEMDB_COT_DECOMPOSE", false),
		CoTMaxSubqueries: clampCoTMaxSubqueries(envInt("MEMDB_COT_MAX_SUBQUERIES", defaultCoTMaxSubqueries)),
		CoTTimeoutMS:     clampCoTTimeoutMS(envInt("MEMDB_COT_TIMEOUT_MS", defaultCoTTimeoutMS)),
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

// ResolveReorgDupStrategy resolves the effective reorganizer dup strategy
// from ReorgDupStrategy and the deprecated ReorgUseHNSW flag.
//
// Priority:
//  1. If ReorgDupStrategy is set (non-empty) it wins. When USE_HNSW is also
//     true, a conflict warning is logged (DUP_STRATEGY takes precedence).
//  2. Else if ReorgUseHNSW is true → "hnsw".
//  3. Else → "auto".
//
// The returned string is one of "auto", "legacy", "hnsw". An unrecognized
// ReorgDupStrategy value logs a warning and falls back to "auto".
func (c *Config) ResolveReorgDupStrategy() string {
	switch {
	case c.ReorgDupStrategy != "":
		if c.ReorgUseHNSW {
			slog.Warn("config: both MEMDB_REORG_DUP_STRATEGY and MEMDB_REORG_USE_HNSW set; DUP_STRATEGY wins",
				slog.String("dup_strategy", c.ReorgDupStrategy),
				slog.Bool("use_hnsw", c.ReorgUseHNSW))
		}
		switch c.ReorgDupStrategy {
		case "auto", "legacy", "hnsw":
			return c.ReorgDupStrategy
		default:
			slog.Warn("config: unrecognized MEMDB_REORG_DUP_STRATEGY value, falling back to auto",
				slog.String("dup_strategy", c.ReorgDupStrategy))
			return "auto"
		}
	case c.ReorgUseHNSW:
		return "hnsw"
	default:
		return "auto"
	}
}

// PortStr returns the port as a string.
func (c *Config) PortStr() string {
	return strconv.Itoa(c.Port)
}

// ── Startup validation (PF-1, PF-5) ───────────────────────────────────────────

// ValidationMode controls which checks Validate performs. Tools that don't
// need a database (e.g. cmd/mcp-server) use ModeTool; the API server uses
// ModeServer which additionally requires PostgresURL.
type ValidationMode int

const (
	// ModeServer validates auth configuration and requires a non-empty
	// PostgresURL. Used by cmd/server/main.go at startup.
	ModeServer ValidationMode = iota
	// ModeTool validates auth configuration only. Used by cmd/mcp-server and
	// other binaries that don't open a Postgres connection.
	ModeTool
)

// authDisabledGauge is a Prometheus gauge that reads 1 when authentication is
// off (AuthEnabled=false) and 0 when on. Operators alert on config_drift when
// this stays at 1 for >5 min in production. Registered on the default registry
// so it is scraped at /metrics alongside other memdb_* series.
var authDisabledGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "memdb_config_auth_disabled",
	Help: "1 when AuthEnabled is false (auth off), 0 when auth is on. " +
		"Alert: config_drift when gauge=1 for >5min in prod.",
})

// Validate checks the config for unsafe or incomplete settings and returns an
// error describing the first problem found. It is called at startup so the
// process fails fast with an actionable message instead of silently running
// with zero auth or a missing database URL.
//
// Auth check (PF-1): when AuthEnabled is false AND MasterKeyHash is empty AND
// DevMode is false, the server refuses to start — a fresh deploy with no env
// vars must not expose all endpoints without credentials. Dev mode
// (MEMDB_DEV=1) is allowed for local development.
//
// PostgresURL check (PF-5): in ModeServer an empty PostgresURL is rejected so
// the failure surfaces at startup rather than on the first DB call. ModeTool
// skips this check for binaries that don't use Postgres.
//
// The auth-disabled gauge is updated as a side effect so observability alerts
// fire regardless of whether validation passes (dev mode) or not.
func (c *Config) Validate(mode ValidationMode) error {
	// Update the auth-disabled observability gauge regardless of outcome.
	if c.AuthEnabled {
		authDisabledGauge.Set(0)
	} else {
		authDisabledGauge.Set(1)
	}

	// PF-1: auth must be configured unless dev mode is explicitly enabled.
	if !c.AuthEnabled && c.MasterKeyHash == "" && !c.DevMode {
		return fmt.Errorf(
			"security: auth is disabled (AUTH_ENABLED=false) and no MASTER_KEY_HASH is set — " +
				"set AUTH_ENABLED=true and MASTER_KEY_HASH, or set MEMDB_DEV=1 for local development",
		)
	}

	// PF-5: server mode requires a Postgres URL so db.NewPostgres doesn't
	// fail late on the first request that touches the database.
	if mode == ModeServer && c.PostgresURL == "" {
		return fmt.Errorf(
			"config: MEMDB_POSTGRES_URL is required for server startup — " +
				"set MEMDB_POSTGRES_URL to a valid Postgres DSN",
		)
	}

	return nil
}
