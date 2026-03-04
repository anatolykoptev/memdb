package proxypool

import (
	"sync"
	"time"
)

// HealthStore persists proxy health data.
// Implementations can back this with Redis, a database, or shared memory
// for distributed proxy health sharing across instances.
type HealthStore interface {
	Get(proxy string) (ProxyHealth, bool)
	Set(proxy string, h ProxyHealth)
	All() map[string]ProxyHealth
}

// memoryHealthStore is the default in-memory implementation.
type memoryHealthStore struct {
	mu   sync.RWMutex
	data map[string]*ProxyHealth
}

// NewMemoryHealthStore creates an in-memory health store.
func NewMemoryHealthStore() HealthStore {
	return &memoryHealthStore{data: make(map[string]*ProxyHealth)}
}

func (m *memoryHealthStore) Get(proxy string) (ProxyHealth, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.data[proxy]
	if !ok {
		return ProxyHealth{}, false
	}
	return *h, true
}

func (m *memoryHealthStore) Set(proxy string, h ProxyHealth) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.data[proxy]
	if ok {
		*stored = h
	} else {
		m.data[proxy] = &h
	}
}

func (m *memoryHealthStore) All() map[string]ProxyHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]ProxyHealth, len(m.data))
	for k, v := range m.data {
		result[k] = *v
	}
	return result
}

// WithHealthStore sets a custom health store for the HealthyProxyPool.
func WithHealthStore(store HealthStore) func(*HealthyProxyPool) {
	return func(hp *HealthyProxyPool) {
		hp.store = store
	}
}

// refactored HealthyProxyPool methods to use store — see healthy.go changes
// The store provides Get/Set/All; the pool handles locking for multi-step operations.

// recordSuccess records a successful request in the store.
func recordSuccessToStore(store HealthStore, proxy string, latency time.Duration) {
	h, _ := store.Get(proxy)
	h.Successes++
	h.TotalLatency += latency
	h.LastUsed = time.Now()
	store.Set(proxy, h)
}

// recordFailure records a failed request and deactivates if threshold exceeded.
func recordFailureToStore(store HealthStore, proxy string, latency time.Duration, cfg HealthyConfig) {
	h, _ := store.Get(proxy)
	h.Failures++
	h.TotalLatency += latency
	h.LastUsed = time.Now()

	total := h.Successes + h.Failures
	if total >= cfg.MinRequests && h.SuccessRate() < (1-cfg.FailureThreshold) {
		h.DeactivateAt = time.Now()
	}
	store.Set(proxy, h)
}
