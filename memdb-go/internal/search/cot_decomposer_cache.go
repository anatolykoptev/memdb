package search

// cot_decomposer_cache.go — small TTL cache + helpers for D11 CoTDecomposer.
// Split out from cot_decomposer.go to keep that file under the 250-line spec
// budget. Keep this file equally tight (no business logic — only data
// structures and pure helpers).

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"
)

// cotDecomposerCache is a tiny TTL cache. Mirrors the iterative_retrieval.go
// pattern but with its own type so the two caches can't accidentally share
// keys.
type cotDecomposerCache struct {
	mu      sync.Mutex
	entries map[string]cotDecomposerCacheEntry
}

type cotDecomposerCacheEntry struct {
	expires time.Time
	subs    []string
}

func newCoTDecomposerCache() *cotDecomposerCache {
	return &cotDecomposerCache{entries: make(map[string]cotDecomposerCacheEntry)}
}

func (c *cotDecomposerCache) get(key string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	// Defensive copy — callers may append to the result.
	out := make([]string, len(e.subs))
	copy(out, e.subs)
	return out, true
}

func (c *cotDecomposerCache) set(key string, subs []string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]string, len(subs))
	copy(cp, subs)
	c.entries[key] = cotDecomposerCacheEntry{expires: time.Now().Add(ttl), subs: cp}
}

// cotCacheKey is sha256(lowercased + trimmed query) — deterministic, collision-
// resistant for any practical query corpus.
func cotCacheKey(query string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(query))))
	return fmt.Sprintf("%x", sum)
}

// normalizeSubqueries trims, dedupes (case-insensitive), drops empties, and
// caps to maxN. Returns nil if nothing usable remains; returns [original] when
// the LLM decided the query was already atomic.
func normalizeSubqueries(subs []string, original string, maxN int) []string {
	out := make([]string, 0, len(subs))
	seen := make(map[string]struct{}, len(subs))
	for _, s := range subs {
		t := strings.TrimSpace(s)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	if len(out) == 1 && strings.EqualFold(out[0], strings.TrimSpace(original)) {
		return out
	}
	if len(out) > maxN {
		out = out[:maxN]
	}
	return out
}
