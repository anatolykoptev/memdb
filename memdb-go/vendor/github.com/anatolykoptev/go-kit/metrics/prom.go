package metrics

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"

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

// promBridge отражает операции Registry в prometheus.DefaultRegisterer.
type promBridge struct {
	namespace  string
	counters   sync.Map // base name → prometheus.Counter or *prometheus.CounterVec
	gauges     sync.Map // base name → prometheus.Gauge or *prometheus.GaugeVec
	histograms sync.Map // base name → prometheus.Histogram or *prometheus.HistogramVec
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
