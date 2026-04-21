package metrics

import "github.com/prometheus/client_golang/prometheus"

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
	if v, ok := b.counters.Load(base); ok {
		return v.(prometheus.Counter) //nolint:forcetypeassert // invariant
	}
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: b.namespace, Name: base,
		Help: "auto-registered via go-kit/metrics bridge",
	})
	actual, loaded := b.counters.LoadOrStore(base, c)
	if !loaded {
		prometheus.DefaultRegisterer.MustRegister(c)
	}
	return actual.(prometheus.Counter) //nolint:forcetypeassert // invariant
}

func (b *promBridge) counterVec(base string, keys []string) *prometheus.CounterVec {
	if v, ok := b.counters.Load(base); ok {
		return v.(*prometheus.CounterVec) //nolint:forcetypeassert // invariant
	}
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: b.namespace, Name: base,
		Help: "auto-registered via go-kit/metrics bridge",
	}, keys)
	actual, loaded := b.counters.LoadOrStore(base, vec)
	if !loaded {
		prometheus.DefaultRegisterer.MustRegister(vec)
	}
	return actual.(*prometheus.CounterVec) //nolint:forcetypeassert // invariant
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
	if v, ok := b.gauges.Load(base); ok {
		return v.(prometheus.Gauge) //nolint:forcetypeassert // invariant
	}
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: b.namespace, Name: base,
		Help: "auto-registered via go-kit/metrics bridge",
	})
	actual, loaded := b.gauges.LoadOrStore(base, g)
	if !loaded {
		prometheus.DefaultRegisterer.MustRegister(g)
	}
	return actual.(prometheus.Gauge) //nolint:forcetypeassert // invariant
}

func (b *promBridge) gaugeVec(base string, keys []string) *prometheus.GaugeVec {
	if v, ok := b.gauges.Load(base); ok {
		return v.(*prometheus.GaugeVec) //nolint:forcetypeassert // invariant
	}
	vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: b.namespace, Name: base,
		Help: "auto-registered via go-kit/metrics bridge",
	}, keys)
	actual, loaded := b.gauges.LoadOrStore(base, vec)
	if !loaded {
		prometheus.DefaultRegisterer.MustRegister(vec)
	}
	return actual.(*prometheus.GaugeVec) //nolint:forcetypeassert // invariant
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
	if v, ok := b.histograms.Load(base); ok {
		return v.(prometheus.Histogram) //nolint:forcetypeassert // invariant
	}
	h := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: b.namespace, Name: base,
		Help:    "auto-registered via go-kit/metrics bridge",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 16),
	})
	actual, loaded := b.histograms.LoadOrStore(base, h)
	if !loaded {
		prometheus.DefaultRegisterer.MustRegister(h)
	}
	return actual.(prometheus.Histogram) //nolint:forcetypeassert // invariant
}

func (b *promBridge) histogramVec(base string, keys []string) *prometheus.HistogramVec {
	if v, ok := b.histograms.Load(base); ok {
		return v.(*prometheus.HistogramVec) //nolint:forcetypeassert // invariant
	}
	vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: b.namespace, Name: base,
		Help:    "auto-registered via go-kit/metrics bridge",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 16),
	}, keys)
	actual, loaded := b.histograms.LoadOrStore(base, vec)
	if !loaded {
		prometheus.DefaultRegisterer.MustRegister(vec)
	}
	return actual.(*prometheus.HistogramVec) //nolint:forcetypeassert // invariant
}

// ---------------------------------------------------------------------------
// TTL cleanup — unregister from prom
// ---------------------------------------------------------------------------

func (b *promBridge) unregister(name string) {
	base, _, _ := parseLabeled(name)
	if c, ok := b.counters.LoadAndDelete(base); ok {
		if col, ok := c.(prometheus.Collector); ok {
			prometheus.DefaultRegisterer.Unregister(col)
		}
	}
	if g, ok := b.gauges.LoadAndDelete(base); ok {
		if col, ok := g.(prometheus.Collector); ok {
			prometheus.DefaultRegisterer.Unregister(col)
		}
	}
	if h, ok := b.histograms.LoadAndDelete(base); ok {
		if col, ok := h.(prometheus.Collector); ok {
			prometheus.DefaultRegisterer.Unregister(col)
		}
	}
}
