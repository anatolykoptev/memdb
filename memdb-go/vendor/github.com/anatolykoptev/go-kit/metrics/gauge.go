package metrics

import (
	"math"
	"sync/atomic"
)

// Gauge tracks a float64 value that can increase or decrease.
// All operations are lock-free using atomic compare-and-swap.
type Gauge struct {
	bits atomic.Uint64
	reg  *Registry // nil for gauges created outside a Registry
	name string
}

// Set sets the gauge to v.
func (g *Gauge) Set(v float64) {
	if g == nil {
		return
	}
	g.bits.Store(math.Float64bits(v))
	if g.reg != nil && g.reg.promBridge != nil {
		g.reg.promBridge.observeGauge(g.name, v, false)
	}
}

// Value returns the current gauge value.
func (g *Gauge) Value() float64 {
	if g == nil {
		return 0
	}
	return math.Float64frombits(g.bits.Load())
}

// Add adds delta to the gauge value.
func (g *Gauge) Add(delta float64) {
	if g == nil {
		return
	}
	var newF float64
	for {
		old := g.bits.Load()
		newF = math.Float64frombits(old) + delta
		if g.bits.CompareAndSwap(old, math.Float64bits(newF)) {
			break
		}
	}
	if g.reg != nil && g.reg.promBridge != nil {
		g.reg.promBridge.observeGauge(g.name, delta, true)
	}
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() { g.Add(1) }

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() { g.Add(-1) }

// Gauge returns the named gauge, creating it on first access.
// Returns a non-nil noop Gauge when called on a nil Registry.
func (r *Registry) Gauge(name string) *Gauge {
	if r == nil {
		return &Gauge{}
	}
	v, _ := r.gauges.LoadOrStore(name, &Gauge{reg: r, name: name})
	return v.(*Gauge) //nolint:forcetypeassert // invariant: only *Gauge stored
}

// GaugeSnapshot returns a copy of all gauges with their current values.
func (r *Registry) GaugeSnapshot() map[string]float64 {
	if r == nil {
		return nil
	}
	m := make(map[string]float64)
	r.gauges.Range(func(k, v any) bool {
		m[k.(string)] = v.(*Gauge).Value() //nolint:forcetypeassert // invariant
		return true
	})
	return m
}
