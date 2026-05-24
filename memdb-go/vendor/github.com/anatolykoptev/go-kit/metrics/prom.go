package metrics

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// parseLabeled разбирает имя метрики в синтаксисе kitmetrics.Label():
//
//	"rpc{service=auth,method=login}" → ("rpc", ["service","method"], ["auth","login"])
//	"wp_rest_calls"                   → ("wp_rest_calls", nil, nil).
//
// Невалидный синтаксис (без закрывающей скобки, пустые пары) возвращается как plain.
func parseLabeled(s string) (name string, keys, vals []string) {
	open := strings.IndexByte(s, '{')
	if open < 0 {
		return s, nil, nil
	}
	if !strings.HasSuffix(s, "}") {
		return s, nil, nil
	}
	name = s[:open]
	inner := s[open+1 : len(s)-1]
	if inner == "" {
		return s, nil, nil
	}
	for _, kv := range strings.Split(inner, ",") {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			return s, nil, nil // malformed
		}
		keys = append(keys, kv[:eq])
		vals = append(vals, kv[eq+1:])
	}
	return name, keys, vals
}

// defaultBucketStart is the first bucket boundary for the seconds-shaped default:
// 1ms (0.001 seconds). ExponentialBuckets(defaultBucketStart, 2, defaultBucketCount) covers
// 1ms–65.5s in 16 doublings, suitable for most RPC and database latency histograms.
const (
	defaultBucketStart = 0.001
	defaultBucketCount = 16
)

// defaultSecondsBuckets is the default bucket set for time-based histograms,
// shaped for sub-millisecond to ~65s latency measurements.
// Used as the fallback in bucketsFor and RegisterHistogram when no custom
// buckets are provided.
var defaultSecondsBuckets = prometheus.ExponentialBuckets(defaultBucketStart, 2, defaultBucketCount)

// histogramConfig holds per-metric histogram options set via RegisterHistogram.
type histogramConfig struct {
	buckets []float64
}

// HistogramOption is a functional option for RegisterHistogram.
type HistogramOption func(*histogramConfig)

// WithBuckets sets explicit histogram bucket boundaries for the named metric.
// Buckets must be strictly ascending and finite (no NaN, no ±Inf).
// nil is accepted as a no-op (the seconds-shaped default will be used).
// An invalid slice panics at RegisterHistogram time (startup), not at first Observe.
//
// Use this for non-time histograms such as bytes, queue depth, or request
// counts — the seconds-shaped default is a poor fit for those units.
//
//	reg.RegisterHistogram("gojob_oversize_bytes",
//	    metrics.WithBuckets([]float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304}))
func WithBuckets(buckets []float64) HistogramOption {
	// Defensive copy: protect against mutation aliasing after registration.
	var copied []float64
	if buckets != nil {
		copied = make([]float64, len(buckets))
		copy(copied, buckets)
	}
	return func(c *histogramConfig) { c.buckets = copied }
}

// validateBuckets checks that buckets is nil (valid no-op) or strictly ascending
// and finite. Returns a non-empty reason string if invalid.
func validateBuckets(buckets []float64) string {
	if buckets == nil {
		return "" // nil = use default, always valid
	}
	for i, b := range buckets {
		if math.IsNaN(b) || math.IsInf(b, 0) {
			return fmt.Sprintf("bucket[%d]=%v is not finite", i, b)
		}
		if i > 0 && buckets[i-1] >= b {
			return fmt.Sprintf("bucket[%d]=%v is not greater than bucket[%d]=%v (must be strictly ascending)", i, b, i-1, buckets[i-1])
		}
	}
	return ""
}

// promBridge отражает операции Registry в prometheus.DefaultRegisterer.
//
// Maps are split per-kind (no-label vs Vec) to avoid type-collision panics
// when the same base name is observed both with and without labels from
// different call sites in the same process. Mixing prometheus.Counter and
// *prometheus.CounterVec under one key caused a forcetypeassert panic in
// production (go-search, ~206 restarts over 52h).
type promBridge struct {
	namespace         string
	countersNoLabel   sync.Map // base name → prometheus.Counter
	countersVec       sync.Map // base name → *prometheus.CounterVec
	gaugesNoLabel     sync.Map // base name → prometheus.Gauge
	gaugesVec         sync.Map // base name → *prometheus.GaugeVec
	histogramsNoLabel sync.Map // base name → prometheus.Histogram
	histogramsVec     sync.Map // base name → *prometheus.HistogramVec
	histogramConfigs  sync.Map // base name → *histogramConfig (pre-registered custom buckets)
}

var nsRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// NewPrometheusRegistry создаёт Registry, дополнительно транслирующий операции
// в prometheus.DefaultRegisterer под указанным namespace. namespace должен
// соответствовать [a-zA-Z_:][a-zA-Z0-9_:]* — иначе panic.
func NewPrometheusRegistry(namespace string) *Registry {
	if !nsRe.MatchString(namespace) {
		panic(fmt.Sprintf("metrics: invalid prometheus namespace %q", namespace))
	}
	return &Registry{promBridge: &promBridge{namespace: namespace}}
}

// MetricsHandler возвращает promhttp.Handler() на prometheus.DefaultRegisterer.
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// FromEnv возвращает *Registry, prometheus-backed если:
//   - PROM_NAMESPACE задан (используется как namespace), или
//   - METRICS_PROM=1 (используется defaultNamespace).
//
// Иначе возвращает NewRegistry() без prom-bridge.
// Паника если METRICS_PROM=1 и defaultNamespace пуст.
func FromEnv(defaultNamespace string) *Registry {
	if ns := os.Getenv("PROM_NAMESPACE"); ns != "" {
		return NewPrometheusRegistry(ns)
	}
	if os.Getenv("METRICS_PROM") == "1" {
		if defaultNamespace == "" {
			panic("metrics: METRICS_PROM=1 requires non-empty defaultNamespace")
		}
		return NewPrometheusRegistry(defaultNamespace)
	}
	return NewRegistry()
}

// RegisterHistogram pre-configures bucket boundaries for the named histogram
// before the first Observe call. Subsequent Observe calls for name will use
// the configured buckets instead of the seconds-shaped default.
//
// Safe to call on a nil *Registry (no-op). Idempotent: a second call with the
// same name is silently ignored — the first registration wins, matching the
// semantics of the underlying prom collector (buckets are locked at first
// Observe).
//
// name must be a plain metric name (no label syntax); labels are applied at
// Observe time via metrics.Label().
//
// Panics at call time (startup/registration) if the supplied WithBuckets value
// is invalid (non-strictly-ascending, NaN, or ±Inf). nil buckets are accepted
// as a no-op (default seconds-shaped buckets are used).
//
// Timing requirement: must be called before the first Observe for name.
// A call after first Observe is silently ignored — buckets are locked at first
// Observe.
//
// Example:
//
//	reg.RegisterHistogram("gojob_oversize_bytes",
//	    metrics.WithBuckets([]float64{1024, 4096, 16384, 65536, 262144, 1048576, 4194304}))
//	reg.Observe("gojob_oversize_bytes", float64(len(payload)))
func (r *Registry) RegisterHistogram(name string, opts ...HistogramOption) {
	if r == nil || r.promBridge == nil {
		return
	}
	cfg := &histogramConfig{
		buckets: defaultSecondsBuckets, // seconds default
	}
	for _, o := range opts {
		o(cfg)
	}
	// Validate buckets at registration time (startup), not deferred to first Observe.
	// This satisfies the CLAUDE.md "no panic outside startup" rule: prometheus.NewHistogram
	// would panic on invalid buckets at first Observe (steady-state) without this check.
	if reason := validateBuckets(cfg.buckets); reason != "" {
		panic(fmt.Sprintf("metrics: invalid buckets for %q: %s", name, reason))
	}
	// LoadOrStore: first registration wins; subsequent calls for the same name
	// are no-ops. This prevents bucket mutation after the first Observe has
	// already locked the prom histogram in place.
	r.promBridge.histogramConfigs.LoadOrStore(name, cfg)
}
