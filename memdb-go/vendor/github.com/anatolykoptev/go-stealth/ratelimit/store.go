package ratelimit

import (
	"sync"
	"time"
)

// Store persists rate limit state per key.
// Implementations can back this with Redis, etcd, or shared memory
// for distributed rate limiting across instances.
type Store interface {
	// Increment atomically increments the request count for a key within the current window.
	// Returns the new count and the window start time.
	// If the window has expired, it should reset the count.
	Increment(key string, window time.Duration) (count int, windowStart time.Time)

	// Count returns the current count without incrementing.
	Count(key string, window time.Duration) (count int, windowStart time.Time)

	// SetBlocked marks a key as blocked until the given time.
	SetBlocked(key string, until time.Time)

	// GetBlocked returns the blocked-until time for a key.
	// Returns zero time if not blocked.
	GetBlocked(key string) time.Time
}

// memoryStore is the default in-memory implementation.
type memoryStore struct {
	mu    sync.Mutex
	state map[string]*storeEntry
}

type storeEntry struct {
	count       int
	windowStart time.Time
	blockedUtil time.Time
}

// NewMemoryStore creates an in-memory rate limit store.
func NewMemoryStore() Store {
	return &memoryStore{state: make(map[string]*storeEntry)}
}

func (m *memoryStore) Increment(key string, window time.Duration) (int, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e := m.getOrCreate(key)
	now := time.Now()

	if now.Sub(e.windowStart) > window {
		e.count = 0
		e.windowStart = now
	}

	e.count++
	return e.count, e.windowStart
}

func (m *memoryStore) Count(key string, window time.Duration) (int, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.state[key]
	if !ok {
		return 0, time.Time{}
	}
	now := time.Now()
	if now.Sub(e.windowStart) > window {
		return 0, time.Time{}
	}
	return e.count, e.windowStart
}

func (m *memoryStore) SetBlocked(key string, until time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.getOrCreate(key)
	e.blockedUtil = until
}

func (m *memoryStore) GetBlocked(key string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.state[key]
	if !ok {
		return time.Time{}
	}
	return e.blockedUtil
}

func (m *memoryStore) getOrCreate(key string) *storeEntry {
	e, ok := m.state[key]
	if !ok {
		e = &storeEntry{windowStart: time.Now()}
		m.state[key] = e
	}
	return e
}
