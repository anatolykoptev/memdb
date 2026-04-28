// Package middleware — cache-level Prometheus metrics (Q5).
package middleware

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	cacheMetricsOnce sync.Once
	cacheInstruments *cacheMetricsStruct
)

type cacheMetricsStruct struct {
	Hits         metric.Int64Counter
	Misses       metric.Int64Counter
	KeyBuildTime metric.Float64Histogram
}

// knownCacheRoutes are the routes pre-registered at zero so Prometheus sees
// them from container start, before any traffic arrives.
var knownCacheRoutes = []string{"search", "chat_complete", "add_fine", "add_buffer", "get_memory"}

func cacheMx() *cacheMetricsStruct {
	cacheMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/cache")
		hits, _ := meter.Int64Counter("memdb.cache.hits_total",
			metric.WithDescription("Cache hits by route"),
		)
		misses, _ := meter.Int64Counter("memdb.cache.misses_total",
			metric.WithDescription("Cache misses by route"),
		)
		keyBuild, _ := meter.Float64Histogram("memdb.cache.key_build_duration_ms",
			metric.WithDescription("Duration of cache key construction in milliseconds"),
			metric.WithUnit("ms"),
			metric.WithExplicitBucketBoundaries(0.01, 0.05, 0.1, 0.5, 1.0, 5.0),
		)
		cacheInstruments = &cacheMetricsStruct{
			Hits:         hits,
			Misses:       misses,
			KeyBuildTime: keyBuild,
		}
		// Pre-register at zero for all known routes so dashboards populate immediately.
		ctx := context.Background()
		for _, route := range knownCacheRoutes {
			hits.Add(ctx, 0, metric.WithAttributes(attribute.String("route", route)))
			misses.Add(ctx, 0, metric.WithAttributes(attribute.String("route", route)))
		}
	})
	return cacheInstruments
}

// recordCacheHit bumps hits_total and returns immediately.
func recordCacheHit(ctx context.Context, route string) {
	mx := cacheMx()
	if mx.Hits == nil {
		return
	}
	mx.Hits.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

// recordCacheMiss bumps misses_total.
func recordCacheMiss(ctx context.Context, route string) {
	mx := cacheMx()
	if mx.Misses == nil {
		return
	}
	mx.Misses.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

// recordCacheKeyBuild records the duration of keyFn execution.
func recordCacheKeyBuild(ctx context.Context, route string, start time.Time) {
	mx := cacheMx()
	if mx.KeyBuildTime == nil {
		return
	}
	ms := float64(time.Since(start).Microseconds()) / 1000.0
	mx.KeyBuildTime.Record(ctx, ms, metric.WithAttributes(attribute.String("route", route)))
}
