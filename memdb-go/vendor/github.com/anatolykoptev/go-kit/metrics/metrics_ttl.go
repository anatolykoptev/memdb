package metrics

import "time"

// SetTTL marks a metric for automatic expiration. After ttl elapses,
// CleanupExpired will remove the metric from all stores (counters, gauges).
// Each call resets the deadline. Use for per-endpoint or per-user metrics
// that become stale.
func (r *Registry) SetTTL(name string, ttl time.Duration) {
	if r == nil {
		return
	}
	r.ttls.Store(name, time.Now().Add(ttl).UnixNano())
}

// IncrWithTTL increments a counter and sets/refreshes its TTL.
func (r *Registry) IncrWithTTL(name string, ttl time.Duration) {
	if r == nil {
		return
	}
	r.counter(name).Add(1)
	r.ttls.Store(name, time.Now().Add(ttl).UnixNano())
	if r.promBridge != nil {
		r.promBridge.observeCounter(name, 1)
	}
}

// AddWithTTL adds delta to a counter and sets/refreshes its TTL.
func (r *Registry) AddWithTTL(name string, delta int64, ttl time.Duration) {
	if r == nil {
		return
	}
	r.counter(name).Add(delta)
	r.ttls.Store(name, time.Now().Add(ttl).UnixNano())
	if r.promBridge != nil {
		r.promBridge.observeCounter(name, float64(delta))
	}
}

// CleanupExpired removes all metrics whose TTL has expired.
// Returns the number of metrics removed.
func (r *Registry) CleanupExpired() int {
	if r == nil {
		return 0
	}
	now := time.Now().UnixNano()
	var removed int
	r.ttls.Range(func(k, v any) bool {
		if v.(int64) < now { //nolint:forcetypeassert // invariant: only int64 stored
			name := k.(string) //nolint:forcetypeassert // invariant
			r.store.Delete(name)
			r.gauges.Delete(name)
			r.ttls.Delete(name)
			if r.promBridge != nil {
				r.promBridge.unregister(name)
			}
			removed++
		}
		return true
	})
	return removed
}
