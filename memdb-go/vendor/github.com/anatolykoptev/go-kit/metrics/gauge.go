package metrics

import (
	"math"
	"sync/atomic"
)

// Gauge tracks a float64 value that can increase or decrease.
// All operations are lock-free using atomic compare-and-swap.
type Gauge struct {
	bits atomic.Uint64
}

// Set sets the gauge to v.
func (g *Gauge) Set(v float64) { g.bits.Store(math.Float64bits(v)) }

// Value returns the current gauge value.
func (g *Gauge) Value() float64 { return math.Float64frombits(g.bits.Load()) }

// Add adds delta to the gauge value.
func (g *Gauge) Add(delta float64) {
	for {
		old := g.bits.Load()
		newVal := math.Float64bits(math.Float64frombits(old) + delta)
		if g.bits.CompareAndSwap(old, newVal) {
			return
		}
	}
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() { g.Add(1) }

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() { g.Add(-1) }

// Gauge returns the named gauge, creating it on first access.
func (r *Registry) Gauge(name string) *Gauge {
	v, _ := r.gauges.LoadOrStore(name, &Gauge{})
	return v.(*Gauge) //nolint:forcetypeassert // invariant: only *Gauge stored
}

// GaugeSnapshot returns a copy of all gauges with their current values.
func (r *Registry) GaugeSnapshot() map[string]float64 {
	m := make(map[string]float64)
	r.gauges.Range(func(k, v any) bool {
		m[k.(string)] = v.(*Gauge).Value() //nolint:forcetypeassert // invariant
		return true
	})
	return m
}
