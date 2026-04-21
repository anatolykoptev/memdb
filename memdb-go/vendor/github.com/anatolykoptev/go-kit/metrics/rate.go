package metrics

import (
	"math"
	"sync/atomic"
	"time"
)

// ewma tick interval — all rates tick at 5-second intervals.
const tickNanos = int64(5 * time.Second)

// EWMA decay factors: α = 1 - e^(-interval/window).
var (
	m1Alpha  = 1 - math.Exp(-5.0/60.0)  // 1-minute
	m5Alpha  = 1 - math.Exp(-5.0/300.0) // 5-minute
	m15Alpha = 1 - math.Exp(-5.0/900.0) // 15-minute
)

// Rate tracks event throughput using exponentially weighted moving averages.
// Reports events/sec over 1-minute, 5-minute, and 15-minute windows.
// Safe for concurrent use. Ticking is lazy — triggered by Update or snapshot reads.
type Rate struct {
	uncounted atomic.Int64
	total     atomic.Int64
	m1        atomic.Uint64 // float64 bits
	m5        atomic.Uint64
	m15       atomic.Uint64
	lastTick  atomic.Int64 // UnixNano
}

func newRate() *Rate {
	r := &Rate{}
	r.lastTick.Store(time.Now().UnixNano())
	return r
}

// Update records n events.
func (r *Rate) Update(n int64) {
	if r == nil {
		return
	}
	r.tickIfNeeded()
	r.uncounted.Add(n)
	r.total.Add(n)
}

// Total returns the total number of events ever recorded.
func (r *Rate) Total() int64 {
	if r == nil {
		return 0
	}
	return r.total.Load()
}

// M1 returns the 1-minute EWMA rate in events/sec.
func (r *Rate) M1() float64 {
	if r == nil {
		return 0
	}
	r.tickIfNeeded()
	return math.Float64frombits(r.m1.Load())
}

// M5 returns the 5-minute EWMA rate in events/sec.
func (r *Rate) M5() float64 {
	if r == nil {
		return 0
	}
	r.tickIfNeeded()
	return math.Float64frombits(r.m5.Load())
}

// M15 returns the 15-minute EWMA rate in events/sec.
func (r *Rate) M15() float64 {
	if r == nil {
		return 0
	}
	r.tickIfNeeded()
	return math.Float64frombits(r.m15.Load())
}

// Snapshot returns a point-in-time snapshot of all rate values.
func (r *Rate) Snapshot() RateSnapshot {
	if r == nil {
		return RateSnapshot{}
	}
	r.tickIfNeeded()
	return RateSnapshot{
		Total: r.total.Load(),
		M1:    math.Float64frombits(r.m1.Load()),
		M5:    math.Float64frombits(r.m5.Load()),
		M15:   math.Float64frombits(r.m15.Load()),
	}
}

// RateSnapshot holds a point-in-time view of rate values.
type RateSnapshot struct {
	Total int64
	M1    float64 // events/sec, 1-minute EWMA
	M5    float64 // events/sec, 5-minute EWMA
	M15   float64 // events/sec, 15-minute EWMA
}

func (r *Rate) tickIfNeeded() {
	now := time.Now().UnixNano()
	last := r.lastTick.Load()
	if last == 0 {
		// zero-value noop Rate (created from a nil Registry) — skip ticking
		return
	}
	elapsed := now - last
	if elapsed < tickNanos {
		return
	}
	if !r.lastTick.CompareAndSwap(last, now) {
		return // another goroutine is ticking
	}
	ticks := int(elapsed / tickNanos)
	for range ticks {
		r.tick()
	}
}

func (r *Rate) tick() {
	count := float64(r.uncounted.Swap(0))
	instantRate := count / (float64(tickNanos) / float64(time.Second))

	m1 := math.Float64frombits(r.m1.Load())
	m5 := math.Float64frombits(r.m5.Load())
	m15 := math.Float64frombits(r.m15.Load())

	r.m1.Store(math.Float64bits(m1 + m1Alpha*(instantRate-m1)))
	r.m5.Store(math.Float64bits(m5 + m5Alpha*(instantRate-m5)))
	r.m15.Store(math.Float64bits(m15 + m15Alpha*(instantRate-m15)))
}

// Rate returns a named rate tracker, creating it on first access.
// Returns a non-nil noop Rate when called on a nil Registry.
func (r *Registry) Rate(name string) *Rate {
	if r == nil {
		return &Rate{}
	}
	v, _ := r.rates.LoadOrStore(name, newRate())
	return v.(*Rate) //nolint:forcetypeassert // invariant: only *Rate stored
}

// RateSnapshot returns snapshots of all rate trackers.
func (r *Registry) RateSnapshot() map[string]RateSnapshot {
	if r == nil {
		return nil
	}
	m := make(map[string]RateSnapshot)
	r.rates.Range(func(k, v any) bool {
		m[k.(string)] = v.(*Rate).Snapshot()
		return true
	})
	return m
}
