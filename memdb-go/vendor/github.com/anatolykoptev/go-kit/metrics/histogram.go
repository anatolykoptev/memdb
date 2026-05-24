package metrics

import (
	"math/rand/v2"
	"sort"
	"sync"
	"time"
)

// Percentile constants used in snapshots.
const (
	p50 = 0.5
	p95 = 0.95
	p99 = 0.99
)

const reservoirSize = 2048

// Reservoir collects a fixed-size uniform sample using Algorithm R (Vitter).
// Provides accurate P50/P95/P99 percentiles without unbounded memory.
// Safe for concurrent use.
type Reservoir struct {
	mu      sync.Mutex
	samples [reservoirSize]float64
	count   int64
	sum     float64
	min     float64
	max     float64
	sorted  bool
}

// Update adds a sample value.
func (h *Reservoir) Update(v float64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	h.count++
	h.sum += v
	if h.count == 1 {
		h.min = v
		h.max = v
	} else {
		if v < h.min {
			h.min = v
		}
		if v > h.max {
			h.max = v
		}
	}

	idx := h.count - 1
	if idx < reservoirSize {
		h.samples[idx] = v
	} else {
		j := rand.Int64N(h.count) //nolint:gosec // G404: reservoir sampling doesn't need crypto rand
		if j < reservoirSize {
			h.samples[j] = v
		}
	}
	h.sorted = false
}

// Percentile returns the value at the given percentile (0.0-1.0).
// Returns 0 if no samples have been recorded.
func (h *Reservoir) Percentile(p float64) float64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	n := min(int(h.count), reservoirSize)
	if n == 0 {
		return 0
	}
	if !h.sorted {
		sort.Float64s(h.samples[:n])
		h.sorted = true
	}
	idx := int(float64(n-1) * p)
	return h.samples[idx]
}

// Count returns the total number of samples recorded.
func (h *Reservoir) Count() int64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

// Snapshot returns a point-in-time histogram summary.
func (h *Reservoir) Snapshot() HistogramSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	n := min(int(h.count), reservoirSize)
	if n == 0 {
		return HistogramSnapshot{}
	}
	if !h.sorted {
		sort.Float64s(h.samples[:n])
		h.sorted = true
	}
	var mean float64
	if h.count > 0 {
		mean = h.sum / float64(h.count)
	}
	return HistogramSnapshot{
		Count: h.count,
		Min:   h.min,
		Max:   h.max,
		Mean:  mean,
		P50:   h.samples[int(float64(n-1)*p50)],
		P95:   h.samples[int(float64(n-1)*p95)],
		P99:   h.samples[int(float64(n-1)*p99)],
	}
}

// HistogramSnapshot holds a point-in-time histogram summary.
type HistogramSnapshot struct {
	Count int64
	Min   float64
	Max   float64
	Mean  float64
	P50   float64
	P95   float64
	P99   float64
}

// Histogram returns a named histogram (reservoir sampler), creating it on first access.
// Returns a non-nil noop Reservoir when called on a nil Registry.
func (r *Registry) Histogram(name string) *Reservoir {
	if r == nil {
		return &Reservoir{}
	}
	v, _ := r.histograms.LoadOrStore(name, &Reservoir{})
	return v.(*Reservoir) //nolint:forcetypeassert // invariant: only *Reservoir stored
}

// HistogramSnapshot returns snapshots of all histograms.
func (r *Registry) HistogramSnapshot() map[string]HistogramSnapshot {
	if r == nil {
		return nil
	}
	m := make(map[string]HistogramSnapshot)
	r.histograms.Range(func(k, v any) bool {
		m[k.(string)] = v.(*Reservoir).Snapshot()
		return true
	})
	return m
}

// ---------------------------------------------------------------------------
// Observe / ObserveSeconds — histogram write helpers on Registry
// ---------------------------------------------------------------------------

// Observe records a raw float64 value into the named histogram.
//
// Routing:
//   - Prometheus-backed registry (NewPrometheusRegistry): routes through the
//     prom bridge — identical to StartTimer.Stop() but without the elapsed-time
//     measurement. Skips the in-mem Reservoir to avoid a gauge+histogram fqName
//     collision in prometheus.DefaultRegisterer.
//   - In-memory registry (NewRegistry): feeds the Reservoir so
//     HistogramSnapshot() reflects the value.
//
// name may be a plain name or a labeled name produced by Label().
func (r *Registry) Observe(name string, v float64) {
	if r == nil {
		return
	}
	if r.promBridge != nil {
		r.promBridge.observeHistogram(name, v)
		return
	}
	r.Histogram(name).Update(v)
}

// ObserveSeconds records a time.Duration as seconds in the named histogram.
// Equivalent to r.Observe(name, d.Seconds()) but communicates intent at the
// call site — use this anywhere you measure latency.
//
//	start := time.Now()
//	resp, err := llm.Complete(ctx, req)
//	reg.ObserveSeconds(metrics.Label("llm_request_seconds", "outcome", classify(err)), time.Since(start))
func (r *Registry) ObserveSeconds(name string, d time.Duration) {
	r.Observe(name, d.Seconds())
}
