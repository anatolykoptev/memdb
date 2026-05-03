package metrics

import (
	"errors"
	"reflect"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// shapeCollisionCounter is a process-wide sentinel that increments whenever a
// metric base name is observed in two incompatible shapes (no-label vs Vec)
// from different call sites. The first-registered shape stays on /metrics; the
// second is silently dropped (kept in our per-kind map so subsequent calls of
// the same shape are idempotent and don't re-attempt prom registration).
//
// Exposing this counter satisfies the "no silent errors on writes" rule from
// CLAUDE.md without pulling a logger dependency into go-kit/metrics.
var (
	shapeCollisionOnce    sync.Once
	shapeCollisionCounter prometheus.Counter
)

func incrShapeCollision() {
	shapeCollisionOnce.Do(func() {
		shapeCollisionCounter = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gokit_metrics_shape_collisions_total",
			Help: "Number of times a base metric name was observed in two incompatible shapes (no-label vs labeled). The first-registered shape wins; subsequent observations of the losing shape are dropped.",
		})
		// Best-effort: if something else already registered this exact name
		// (different go-kit version vendored twice), swallow the error.
		if err := prometheus.DefaultRegisterer.Register(shapeCollisionCounter); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				if existing, ok := are.ExistingCollector.(prometheus.Counter); ok {
					shapeCollisionCounter = existing
				}
			}
		}
	})
	if shapeCollisionCounter != nil {
		shapeCollisionCounter.Inc()
	}
}

// registerOrCollide attempts to register c with prometheus.DefaultRegisterer.
// Returns (collector to use, isCollision). On AlreadyRegisteredError with a
// type-compatible existing collector, returns the existing one (idempotent).
// On AlreadyRegisteredError with an incompatible shape (the production crash
// scenario), returns c unchanged with isCollision=true — the caller keeps c in
// its per-kind map so subsequent same-shape calls don't re-register, but c is
// NOT on /metrics. Same-shape collisions are not counted.
func registerOrCollide(c prometheus.Collector) (prometheus.Collector, bool) {
	err := prometheus.DefaultRegisterer.Register(c)
	if err == nil {
		return c, false
	}
	var are prometheus.AlreadyRegisteredError
	if !errors.As(err, &are) {
		// Plain registration error — almost always a fqName-with-different-
		// label-shape conflict (registry.go:315). Prometheus returns a plain
		// fmt.Errorf, not AlreadyRegisteredError, in that case. Treat as a
		// shape collision: keep c off-meter, return it for idempotent reuse.
		// This avoids the production crash in go-search.
		return c, true
	}
	// Same-shape duplicate (e.g. two NewPrometheusRegistry with the same
	// namespace) — reuse the existing collector.
	// Same-shape duplicate? Reuse existing collector. Compared by reflect type
	// to sidestep prometheus.Counter / Gauge interface overlap (both embed
	// Metric+Collector and share Inc/Add).
	if reflect.TypeOf(c) == reflect.TypeOf(are.ExistingCollector) {
		return are.ExistingCollector, false
	}
	// Shape collision: same fqName, different shape. Bump sentinel and keep c
	// (off-meter) so the calling site is at least idempotent.
	return c, true
}

// ---------------------------------------------------------------------------
// Counter bridge
// ---------------------------------------------------------------------------

// observeCounter инкрементирует prom counter, создавая CounterVec/Counter
// при первом обращении. delta — значение прироста (обычно 1).
func (b *promBridge) observeCounter(name string, delta float64) {
	base, keys, vals := parseLabeled(name)
	if len(keys) == 0 {
		c := b.counterNoLabels(base)
		c.Add(delta)
		return
	}
	vec := b.counterVec(base, keys)
	vec.WithLabelValues(vals...).Add(delta)
}

func (b *promBridge) counterNoLabels(base string) prometheus.Counter {
	if v, ok := b.countersNoLabel.Load(base); ok {
		return v.(prometheus.Counter) //nolint:forcetypeassert // map holds only prometheus.Counter
	}
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: b.namespace, Name: base,
		Help: "auto-registered via go-kit/metrics bridge",
	})
	actual, loaded := b.countersNoLabel.LoadOrStore(base, c)
	if !loaded {
		col, collided := registerOrCollide(c)
		if collided {
			incrShapeCollision()
		}
		// Replace stored value with the canonical one returned by prom.
		b.countersNoLabel.Store(base, col)
		return col.(prometheus.Counter) //nolint:forcetypeassert // map holds only prometheus.Counter
	}
	return actual.(prometheus.Counter) //nolint:forcetypeassert // map holds only prometheus.Counter
}

func (b *promBridge) counterVec(base string, keys []string) *prometheus.CounterVec {
	if v, ok := b.countersVec.Load(base); ok {
		return v.(*prometheus.CounterVec) //nolint:forcetypeassert // map holds only *prometheus.CounterVec
	}
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: b.namespace, Name: base,
		Help: "auto-registered via go-kit/metrics bridge",
	}, keys)
	actual, loaded := b.countersVec.LoadOrStore(base, vec)
	if !loaded {
		col, collided := registerOrCollide(vec)
		if collided {
			incrShapeCollision()
		}
		b.countersVec.Store(base, col)
		return col.(*prometheus.CounterVec) //nolint:forcetypeassert // map holds only *prometheus.CounterVec
	}
	return actual.(*prometheus.CounterVec) //nolint:forcetypeassert // map holds only *prometheus.CounterVec
}

// ---------------------------------------------------------------------------
// Gauge bridge
// ---------------------------------------------------------------------------

func (b *promBridge) observeGauge(name string, value float64, opAdd bool) {
	base, keys, vals := parseLabeled(name)
	if len(keys) == 0 {
		g := b.gaugeNoLabels(base)
		if opAdd {
			g.Add(value)
		} else {
			g.Set(value)
		}
		return
	}
	vec := b.gaugeVec(base, keys)
	if opAdd {
		vec.WithLabelValues(vals...).Add(value)
	} else {
		vec.WithLabelValues(vals...).Set(value)
	}
}

func (b *promBridge) gaugeNoLabels(base string) prometheus.Gauge {
	if v, ok := b.gaugesNoLabel.Load(base); ok {
		return v.(prometheus.Gauge) //nolint:forcetypeassert // map holds only prometheus.Gauge
	}
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: b.namespace, Name: base,
		Help: "auto-registered via go-kit/metrics bridge",
	})
	actual, loaded := b.gaugesNoLabel.LoadOrStore(base, g)
	if !loaded {
		col, collided := registerOrCollide(g)
		if collided {
			incrShapeCollision()
		}
		b.gaugesNoLabel.Store(base, col)
		return col.(prometheus.Gauge) //nolint:forcetypeassert // map holds only prometheus.Gauge
	}
	return actual.(prometheus.Gauge) //nolint:forcetypeassert // map holds only prometheus.Gauge
}

func (b *promBridge) gaugeVec(base string, keys []string) *prometheus.GaugeVec {
	if v, ok := b.gaugesVec.Load(base); ok {
		return v.(*prometheus.GaugeVec) //nolint:forcetypeassert // map holds only *prometheus.GaugeVec
	}
	vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: b.namespace, Name: base,
		Help: "auto-registered via go-kit/metrics bridge",
	}, keys)
	actual, loaded := b.gaugesVec.LoadOrStore(base, vec)
	if !loaded {
		col, collided := registerOrCollide(vec)
		if collided {
			incrShapeCollision()
		}
		b.gaugesVec.Store(base, col)
		return col.(*prometheus.GaugeVec) //nolint:forcetypeassert // map holds only *prometheus.GaugeVec
	}
	return actual.(*prometheus.GaugeVec) //nolint:forcetypeassert // map holds only *prometheus.GaugeVec
}

// ---------------------------------------------------------------------------
// Histogram bridge
// ---------------------------------------------------------------------------

func (b *promBridge) observeHistogram(name string, seconds float64) {
	base, keys, vals := parseLabeled(name)
	if len(keys) == 0 {
		b.histogramNoLabels(base).Observe(seconds)
		return
	}
	b.histogramVec(base, keys).WithLabelValues(vals...).Observe(seconds)
}

func (b *promBridge) histogramNoLabels(base string) prometheus.Histogram {
	if v, ok := b.histogramsNoLabel.Load(base); ok {
		return v.(prometheus.Histogram) //nolint:forcetypeassert // map holds only prometheus.Histogram
	}
	h := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: b.namespace, Name: base,
		Help:    "auto-registered via go-kit/metrics bridge",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 16),
	})
	actual, loaded := b.histogramsNoLabel.LoadOrStore(base, h)
	if !loaded {
		col, collided := registerOrCollide(h)
		if collided {
			incrShapeCollision()
		}
		b.histogramsNoLabel.Store(base, col)
		return col.(prometheus.Histogram) //nolint:forcetypeassert // map holds only prometheus.Histogram
	}
	return actual.(prometheus.Histogram) //nolint:forcetypeassert // map holds only prometheus.Histogram
}

func (b *promBridge) histogramVec(base string, keys []string) *prometheus.HistogramVec {
	if v, ok := b.histogramsVec.Load(base); ok {
		return v.(*prometheus.HistogramVec) //nolint:forcetypeassert // map holds only *prometheus.HistogramVec
	}
	vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: b.namespace, Name: base,
		Help:    "auto-registered via go-kit/metrics bridge",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 16),
	}, keys)
	actual, loaded := b.histogramsVec.LoadOrStore(base, vec)
	if !loaded {
		col, collided := registerOrCollide(vec)
		if collided {
			incrShapeCollision()
		}
		b.histogramsVec.Store(base, col)
		return col.(*prometheus.HistogramVec) //nolint:forcetypeassert // map holds only *prometheus.HistogramVec
	}
	return actual.(*prometheus.HistogramVec) //nolint:forcetypeassert // map holds only *prometheus.HistogramVec
}

// ---------------------------------------------------------------------------
// TTL cleanup — unregister from prom
// ---------------------------------------------------------------------------

// unregister removes any prom collector(s) registered under base. Both the
// no-label and Vec maps are cleaned because the same base name may exist as
// either form (or, after a shape collision, in both maps with one shape
// off-meter).
func (b *promBridge) unregister(name string) {
	base, _, _ := parseLabeled(name)
	unregisterFrom(&b.countersNoLabel, base)
	unregisterFrom(&b.countersVec, base)
	unregisterFrom(&b.gaugesNoLabel, base)
	unregisterFrom(&b.gaugesVec, base)
	unregisterFrom(&b.histogramsNoLabel, base)
	unregisterFrom(&b.histogramsVec, base)
}

func unregisterFrom(m *sync.Map, base string) {
	if v, ok := m.LoadAndDelete(base); ok {
		if col, ok := v.(prometheus.Collector); ok {
			prometheus.DefaultRegisterer.Unregister(col)
		}
	}
}
