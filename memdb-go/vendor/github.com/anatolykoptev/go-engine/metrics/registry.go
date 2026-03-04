// Package metrics provides atomic counter registry for service telemetry.
//
// This package delegates to go-kit/metrics, providing all its features:
// counters, gauges, histograms, rates, timers, TTL, and formatted output.
package metrics

import kitmetrics "github.com/anatolykoptev/go-kit/metrics"

// Registry is a thread-safe collection of named atomic counters and gauges.
// Delegates to go-kit/metrics.Registry.
type Registry = kitmetrics.Registry

// New creates a new empty Registry.
func New() *Registry {
	return kitmetrics.NewRegistry()
}
