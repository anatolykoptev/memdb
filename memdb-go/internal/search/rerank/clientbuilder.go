// Package rerank — go-kit/rerank v2 client construction (M13 W1).
//
// clientbuilder.go consolidates the two production CE client construction
// sites (server_init_search.go for SearchService.RerankClient and
// server_init.go for the reorganizer's CE-precompute pass) onto a single
// helper that wires the v2 functional-options surface from go-kit/rerank.
//
// v1 callers (`rerank.New(rerank.Config{...}, logger)`) remain untouched —
// the v2 surface is purely additive. Tests still construct clients via the
// v1 API to keep diffs minimal.
//
// Env-driven knobs (all opt-in, defaults preserve v1 behaviour):
//
//	MEMDB_RERANK_RETRY_ATTEMPTS         int        default 3   (G1)
//	MEMDB_RERANK_RETRY_BASE_BACKOFF     duration   default 200ms (G1)
//	MEMDB_RERANK_RETRY_MAX_BACKOFF      duration   default 2s    (G1)
//	MEMDB_RERANK_CIRCUIT_ENABLED        bool       default false (G1)
//	MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD int        default 5     (G1)
//	MEMDB_RERANK_CIRCUIT_OPEN_DURATION  duration   default 30s   (G1)
//	MEMDB_RERANK_CIRCUIT_HALFOPEN_PROBES int       default 1     (G1)
//	MEMDB_RERANK_NORMALIZE              "none|minmax|zscore"  default none (G2)
//	MEMDB_RERANK_SERVER_NORMALIZE       "none|sigmoid"        default none (G2)
//	MEMDB_RERANK_MAX_TOKENS_PER_DOC     int        default 0 (off) (G2)
//	MEMDB_RERANK_QUERY_INSTRUCTION      string     default ""      (G2)
//	MEMDB_RERANK_DOC_INSTRUCTION        string     default ""      (G2)
//
// The circuit breaker stays off by default — flipping it on without
// observability for embed-server stability would shift failures behind a
// "circuit_open" reason without any operator signal. Operators flip
// MEMDB_RERANK_CIRCUIT_ENABLED=true once dashboards for the new metrics
// are in place.
//
// G3 cascade and G4 cache/multi-query are NOT wired here; they require
// orchestration changes that land in M13.X follow-ups (cascade as a
// separate `staged_backend` mode, multi-query inside the J3-Q stream).

package rerank

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	gokitrerank "github.com/anatolykoptev/go-kit/rerank"
)

// ClientConfig is the static side of the cross-encoder client config —
// the bits that the `config.Config` struct already exposes via the
// CROSS_ENCODER_* env vars. Kept as its own struct so the env-driven
// resilience / quality knobs (MEMDB_RERANK_*) flow in via NewClient
// without polluting the long-lived `config.Config`.
//
// Zero URL disables the client entirely (rerank.Client.Available returns
// false, and the CrossEncoder strategy short-circuits).
type ClientConfig struct {
	URL            string
	Model          string
	APIKey         string
	Timeout        time.Duration
	MaxDocs        int
	MaxCharsPerDoc int
}

// NewClient builds a *gokitrerank.Client wired with M13 W1 v2 options
// (retry / circuit / normalize / instruction / token-truncate) plus a
// memdb-flavoured Observer that bumps the OTel counters
// (memdb.search.rerank_*).
//
// logger: currently unused. Upstream go-kit/rerank only attaches a slog
// logger via the v1 New(cfg, logger) path; the v2 NewClient(url, opts...)
// constructor takes no logger argument and silently drops "rerank failed"
// warnings. We accept it here so the call-sites need only swap struct
// fields, and so a future upstream WithLogger option can be wired without
// touching the two production sites. Observer hooks (OnAfterCall with
// status=degraded, OnRetry, OnCircuitTransition) provide equivalent
// per-event signal via OTel counters.
//
// Behaviour parity: with all MEMDB_RERANK_* unset, output matches the
// pre-M13 v1 client byte-for-byte EXCEPT that the default retry-on-5xx
// policy is now active (3 attempts, exp backoff). To opt out:
// MEMDB_RERANK_RETRY_ATTEMPTS=1.
func NewClient(cfg ClientConfig, logger *slog.Logger) *gokitrerank.Client {
	_ = logger // see godoc above; reserved for upstream WithLogger option.
	opts := []gokitrerank.Opt{
		gokitrerank.WithModel(cfg.Model),
		gokitrerank.WithAPIKey(cfg.APIKey),
		gokitrerank.WithTimeout(cfg.Timeout),
		gokitrerank.WithMaxDocs(cfg.MaxDocs),
		gokitrerank.WithMaxCharsPerDoc(cfg.MaxCharsPerDoc),
	}

	// G1 — retry policy. Default-on (matches go-kit's defaultRetryPolicy);
	// the env knob covers attempt count + backoff bounds. Backoff multiplier
	// + jitter + retryable-status set stay at go-kit defaults to avoid
	// surface area creep.
	retry := envRetryPolicy()
	opts = append(opts, gokitrerank.WithRetry(retry))

	// G1 — circuit breaker. Off by default; flip via env.
	if envBool("MEMDB_RERANK_CIRCUIT_ENABLED", false) {
		opts = append(opts, gokitrerank.WithCircuit(envCircuitConfig()))
	}

	// G2 — client-side normalize (minmax|zscore). Sigmoid lives on the
	// server side via WithServerNormalize.
	if mode := envNormalizeMode("MEMDB_RERANK_NORMALIZE"); mode != gokitrerank.NormalizeNone {
		opts = append(opts, gokitrerank.WithNormalize(mode))
	}

	// G2 — server-side normalize (forwarded as `normalize` field in JSON).
	if sn := envServerNormalize("MEMDB_RERANK_SERVER_NORMALIZE"); sn != gokitrerank.ServerNormalizeNone {
		opts = append(opts, gokitrerank.WithServerNormalize(sn))
	}

	// G2 — token-aware truncation. 0 = disabled (defer to char-truncation
	// from the long-lived CROSS_ENCODER_MAX_CHARS_PER_DOC config).
	if n := envInt("MEMDB_RERANK_MAX_TOKENS_PER_DOC", 0); n > 0 {
		opts = append(opts, gokitrerank.WithMaxTokensPerDoc(n))
	}

	// G2 — instruction prefixes. Both empty → no-op for bge-m3 / gte-multi
	// (their model cards explicitly say "no prefix"). Ops can wire bge-v1.5
	// or E5-style task descriptions per model without a code change.
	qInstr := os.Getenv("MEMDB_RERANK_QUERY_INSTRUCTION")
	dInstr := os.Getenv("MEMDB_RERANK_DOC_INSTRUCTION")
	if qInstr != "" || dInstr != "" {
		opts = append(opts, gokitrerank.WithInstruction(qInstr, dInstr))
	}

	// Observer — bridges go-kit hooks to OTel counters declared in the
	// parent search package. Unset hooks are tolerated (no-ops).
	opts = append(opts, gokitrerank.WithObserver(newOTelObserver(cfg.Model)))

	return gokitrerank.NewClient(cfg.URL, opts...)
}

// envInt reads a positive int from env; non-numeric / unset → fallback.
// Local copy of internal/config/envInt because importing that package here
// would create a cycle (config → search/rerank for the type, search/rerank
// → config for the helper).
func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// envBool reads a bool ("true"/"false"/"1"/"0"/...) from env.
func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

// envDuration reads a duration string ("200ms", "2s", ...) from env.
func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// envRetryPolicy builds a RetryPolicy from MEMDB_RERANK_RETRY_* env vars.
// Falls back to the go-kit defaults for any unset / malformed value.
//
// Backoff multiplier + jitter + retryable-status list intentionally NOT
// exposed via env — they're invariants of the embed-server contract
// (5xx == transient). Operators who need to tune those should construct
// the client directly.
func envRetryPolicy() gokitrerank.RetryPolicy {
	const (
		defaultMaxAttempts = 3
		defaultBaseBackoff = 200 * time.Millisecond
		defaultMaxBackoff  = 2 * time.Second
		defaultMultiplier  = 2.0
		defaultJitter      = 0.1
	)
	return gokitrerank.RetryPolicy{
		MaxAttempts:     envInt("MEMDB_RERANK_RETRY_ATTEMPTS", defaultMaxAttempts),
		BaseBackoff:     envDuration("MEMDB_RERANK_RETRY_BASE_BACKOFF", defaultBaseBackoff),
		MaxBackoff:      envDuration("MEMDB_RERANK_RETRY_MAX_BACKOFF", defaultMaxBackoff),
		Multiplier:      defaultMultiplier,
		Jitter:          defaultJitter,
		RetryableStatus: []int{500, 502, 503, 504},
	}
}

// envCircuitConfig builds a CircuitConfig from MEMDB_RERANK_CIRCUIT_* env vars.
func envCircuitConfig() gokitrerank.CircuitConfig {
	const (
		defaultFailThreshold  = 5
		defaultOpenDuration   = 30 * time.Second
		defaultHalfOpenProbes = 1
	)
	return gokitrerank.CircuitConfig{
		FailThreshold:  envInt("MEMDB_RERANK_CIRCUIT_FAIL_THRESHOLD", defaultFailThreshold),
		OpenDuration:   envDuration("MEMDB_RERANK_CIRCUIT_OPEN_DURATION", defaultOpenDuration),
		HalfOpenProbes: envInt("MEMDB_RERANK_CIRCUIT_HALFOPEN_PROBES", defaultHalfOpenProbes),
	}
}

// envNormalizeMode parses MEMDB_RERANK_NORMALIZE into a NormalizeMode.
// Sigmoid is intentionally NOT supported here — use
// MEMDB_RERANK_SERVER_NORMALIZE=sigmoid to push it server-side.
//
// Unknown / empty / "none" → NormalizeNone (default v1 behaviour).
func envNormalizeMode(key string) gokitrerank.NormalizeMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "minmax":
		return gokitrerank.NormalizeMinMax
	case "zscore":
		return gokitrerank.NormalizeZScore
	default:
		return gokitrerank.NormalizeNone
	}
}

// envServerNormalize parses MEMDB_RERANK_SERVER_NORMALIZE.
// "" / "none" → ServerNormalizeNone (omit field on wire);
// "sigmoid"   → ServerNormalizeSigmoid.
// Unknown values are treated as None to keep behaviour stable in face of
// typos (logs would be the right place to flag, but we don't have a
// slog here without leaking a logger; ops will see the missing
// rerank_server_normalize=sigmoid in metrics and follow up).
func envServerNormalize(key string) gokitrerank.ServerNormalize {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "sigmoid":
		return gokitrerank.ServerNormalizeSigmoid
	default:
		return gokitrerank.ServerNormalizeNone
	}
}
