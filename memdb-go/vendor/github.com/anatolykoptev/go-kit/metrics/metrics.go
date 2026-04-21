// Package metrics provides lightweight atomic counters and gauges for operational observability.
// All operations are safe for concurrent use. Zero external dependencies.
// Each Registry is independent — use NewRegistry() per component or share globally.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Registry holds named atomic counters and gauges.
type Registry struct {
	store      sync.Map    // counters: *atomic.Int64
	gauges     sync.Map    // gauges: *Gauge
	rates      sync.Map    // rates: *Rate
	histograms sync.Map    // histograms: *Reservoir
	ttls       sync.Map    // name -> int64 (deadline UnixNano)
	promBridge *promBridge // nil unless created via NewPrometheusRegistry
}

// NewRegistry creates a new empty counter registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// counter returns the *atomic.Int64 for name, creating it on first access.
func (r *Registry) counter(name string) *atomic.Int64 {
	v, _ := r.store.LoadOrStore(name, new(atomic.Int64))
	return v.(*atomic.Int64) //nolint:forcetypeassert // invariant: only *atomic.Int64 stored
}

// Incr increments the named counter by 1.
func (r *Registry) Incr(name string) {
	if r == nil {
		return
	}
	r.counter(name).Add(1)
	if r.promBridge != nil {
		r.promBridge.observeCounter(name, 1)
	}
}

// Add adds delta to the named counter.
func (r *Registry) Add(name string, delta int64) {
	if r == nil {
		return
	}
	r.counter(name).Add(delta)
	if r.promBridge != nil {
		r.promBridge.observeCounter(name, float64(delta))
	}
}

// Value returns the current value of the named counter.
func (r *Registry) Value(name string) int64 {
	if r == nil {
		return 0
	}
	return r.counter(name).Load()
}

// Snapshot returns a copy of all counters with their current values.
// Only counters that have been written at least once are included.
func (r *Registry) Snapshot() map[string]int64 {
	if r == nil {
		return nil
	}
	m := make(map[string]int64)
	r.store.Range(func(k, v any) bool {
		m[k.(string)] = v.(*atomic.Int64).Load() //nolint:forcetypeassert // invariant
		return true
	})
	return m
}

// SnapshotAndReset returns current counter values and atomically resets them to zero.
func (r *Registry) SnapshotAndReset() map[string]int64 {
	if r == nil {
		return nil
	}
	m := make(map[string]int64)
	r.store.Range(func(k, v any) bool {
		m[k.(string)] = v.(*atomic.Int64).Swap(0) //nolint:forcetypeassert // invariant
		return true
	})
	return m
}

// Reset clears all counters and gauges. Intended for tests.
func (r *Registry) Reset() {
	if r == nil {
		return
	}
	r.store.Range(func(k, _ any) bool { r.store.Delete(k); return true })
	r.gauges.Range(func(k, _ any) bool { r.gauges.Delete(k); return true })
	r.rates.Range(func(k, _ any) bool { r.rates.Delete(k); return true })
	r.histograms.Range(func(k, _ any) bool { r.histograms.Delete(k); return true })
	r.ttls.Range(func(k, _ any) bool { r.ttls.Delete(k); return true })
}

// Format returns a human-readable summary of all counters and gauges, sorted by name.
func (r *Registry) Format() string {
	if r == nil {
		return ""
	}
	counters := r.Snapshot()
	gauges := r.GaugeSnapshot()
	if len(counters) == 0 && len(gauges) == 0 {
		return ""
	}

	type entry struct {
		name string
		text string
	}
	entries := make([]entry, 0, len(counters)+len(gauges))
	for k, v := range counters {
		entries = append(entries, entry{k, fmt.Sprintf("%s=%d", k, v)})
	}
	for k, v := range gauges {
		entries = append(entries, entry{k, fmt.Sprintf("%s=%.2f", k, v)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(e.text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// TrackOperation increments callCounter, runs fn, and increments errCounter
// if fn returns a non-nil error. The error from fn is always returned unchanged.
func (r *Registry) TrackOperation(callCounter, errCounter string, fn func() error) error {
	if r == nil {
		return fn()
	}
	r.Incr(callCounter)
	if err := fn(); err != nil {
		r.Incr(errCounter)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Timer
// ---------------------------------------------------------------------------

// TimerHandle tracks a started timer. Call Stop to record the duration.
type TimerHandle struct {
	reg   *Registry
	name  string
	start time.Time
}

// StartTimer starts a timer for the named metric.
// Call Stop to record the duration. Usage: defer reg.StartTimer("api.latency").Stop()
func (r *Registry) StartTimer(name string) *TimerHandle {
	if r == nil {
		return &TimerHandle{start: time.Now()}
	}
	return &TimerHandle{reg: r, name: name, start: time.Now()}
}

// Stop records the elapsed duration since StartTimer.
// In non-prom Registry: sets gauge "name" to duration in milliseconds (float64
// for sub-ms precision) and increments counter "name.count".
// In prom-backed Registry (NewPrometheusRegistry): observes a histogram under
// "name" in seconds and skips the legacy gauge/counter writes — prom histograms
// natively expose `_count`/`_sum`/`_bucket`, and a gauge+histogram cannot
// co-exist under the same full name in DefaultRegisterer.
func (h *TimerHandle) Stop() time.Duration {
	d := time.Since(h.start)
	if h.reg == nil {
		return d
	}
	if h.reg.promBridge != nil {
		h.reg.promBridge.observeHistogram(h.name, d.Seconds())
		return d
	}
	h.reg.Gauge(h.name).Set(float64(d.Microseconds()) / 1000.0) //nolint:mnd // ms conversion
	h.reg.Incr(h.name + ".count")
	return d
}
