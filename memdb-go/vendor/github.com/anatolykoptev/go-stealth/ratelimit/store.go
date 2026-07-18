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
	clock func() time.Time
}

type storeEntry struct {
	count       int
	windowStart time.Time
	blockedUtil time.Time
}

// NewMemoryStore creates an in-memory rate limit store backed by the wall clock.
func NewMemoryStore() Store {
	return newMemoryStore(time.Now)
}

// newMemoryStore creates an in-memory store with an injectable clock. The
// Limiter wires its resolved clock here so window arithmetic in the store and
// the blocked-until checks in the Limiter share one time source (deterministic
// under a fake clock in tests).
func newMemoryStore(clock func() time.Time) *memoryStore {
	if clock == nil {
		clock = time.Now
	}
	return &memoryStore{state: make(map[string]*storeEntry), clock: clock}
}

func (m *memoryStore) Increment(key string, window time.Duration) (int, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e := m.getOrCreate(key)
	now := m.clock()

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
	now := m.clock()
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
		e = &storeEntry{windowStart: m.clock()}
		m.state[key] = e
	}
	return e
}
