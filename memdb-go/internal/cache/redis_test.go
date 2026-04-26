package cache

import "testing"

func TestSearchCacheKey_Valid(t *testing.T) {
	f := searchKeyFields{
		UserID: "memos",
		Query:  "test",
		TopK:   5,
		Dedup:  "mmr",
	}
	key := SearchCacheKey(f)

	if key == "" {
		t.Error("expected non-empty cache key")
	}
	if len(key) < 20 {
		t.Error("cache key too short")
	}

	// Same input produces same key
	key2 := SearchCacheKey(f)
	if key != key2 {
		t.Error("same input should produce same key")
	}
}

func TestSearchCacheKey_DifferentInputs(t *testing.T) {
	key1 := SearchCacheKey(searchKeyFields{UserID: "memos", Query: "test1"})
	key2 := SearchCacheKey(searchKeyFields{UserID: "memos", Query: "test2"})

	if key1 == key2 {
		t.Error("different queries should produce different keys")
	}
}

func TestSearchCacheKey_DiffersByLevel(t *testing.T) {
	base := searchKeyFields{UserID: "memos", Query: "test", TopK: 5, Dedup: "no"}

	keyAll := SearchCacheKey(base)
	keyL1 := SearchCacheKey(searchKeyFields{UserID: base.UserID, Query: base.Query, TopK: base.TopK, Dedup: base.Dedup, Level: "l1"})
	keyL2 := SearchCacheKey(searchKeyFields{UserID: base.UserID, Query: base.Query, TopK: base.TopK, Dedup: base.Dedup, Level: "l2"})
	keyL3 := SearchCacheKey(searchKeyFields{UserID: base.UserID, Query: base.Query, TopK: base.TopK, Dedup: base.Dedup, Level: "l3"})

	if keyAll == keyL1 {
		t.Error("full search and l1 should produce different keys")
	}
	if keyL1 == keyL2 {
		t.Error("l1 and l2 should produce different keys")
	}
	if keyL2 == keyL3 {
		t.Error("l2 and l3 should produce different keys")
	}
}

func TestSearchCacheKey_DiffersByAgentID(t *testing.T) {
	base := searchKeyFields{UserID: "memos", Query: "test", TopK: 5, Dedup: "no"}

	keyNoAgent := SearchCacheKey(base)
	keyAgent1 := SearchCacheKey(searchKeyFields{UserID: base.UserID, Query: base.Query, TopK: base.TopK, Dedup: base.Dedup, AgentID: "agent-1"})
	keyAgent2 := SearchCacheKey(searchKeyFields{UserID: base.UserID, Query: base.Query, TopK: base.TopK, Dedup: base.Dedup, AgentID: "agent-2"})

	if keyNoAgent == keyAgent1 {
		t.Error("no agent and agent-1 should produce different keys")
	}
	if keyAgent1 == keyAgent2 {
		t.Error("agent-1 and agent-2 should produce different keys")
	}
}

func TestSearchCacheKey_DiffersByPrefTopK(t *testing.T) {
	base := searchKeyFields{UserID: "memos", Query: "test", TopK: 5, Dedup: "no"}

	keyDefault := SearchCacheKey(base)
	keyPref3 := SearchCacheKey(searchKeyFields{UserID: base.UserID, Query: base.Query, TopK: base.TopK, Dedup: base.Dedup, PrefTopK: 3})
	keyPref10 := SearchCacheKey(searchKeyFields{UserID: base.UserID, Query: base.Query, TopK: base.TopK, Dedup: base.Dedup, PrefTopK: 10})

	if keyDefault == keyPref3 {
		t.Error("default pref_top_k and pref_top_k=3 should produce different keys")
	}
	if keyPref3 == keyPref10 {
		t.Error("pref_top_k=3 and pref_top_k=10 should produce different keys")
	}
}

func TestSearchCacheKey_StableForSameInputs(t *testing.T) {
	f := searchKeyFields{
		UserID:   "user-123",
		Query:    "outdoor activities",
		TopK:     10,
		Dedup:    "mmr",
		Level:    "l2",
		AgentID:  "agent-abc",
		PrefTopK: 5,
	}

	keys := make([]string, 5)
	for i := range keys {
		keys[i] = SearchCacheKey(f)
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] != keys[0] {
			t.Errorf("key[%d] = %q differs from key[0] = %q", i, keys[i], keys[0])
		}
	}
}

func TestSearchCacheKey_V2Prefix(t *testing.T) {
	key := SearchCacheKey(searchKeyFields{UserID: "u", Query: "q"})
	const prefix = "memdb:cache:search:v2:"
	if len(key) < len(prefix) || key[:len(prefix)] != prefix {
		t.Errorf("expected key to start with %q, got %q", prefix, key)
	}
}

func TestParseSearchCacheKey_Valid(t *testing.T) {
	body := []byte(`{"user_id":"memos","query":"test","top_k":5,"dedup":"mmr","level":"l1","agent_id":"a1","pref_top_k":3}`)
	f, err := ParseSearchCacheKey(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.UserID != "memos" {
		t.Errorf("UserID: got %q, want %q", f.UserID, "memos")
	}
	if f.Query != "test" {
		t.Errorf("Query: got %q, want %q", f.Query, "test")
	}
	if f.TopK != 5 {
		t.Errorf("TopK: got %v, want 5", f.TopK)
	}
	if f.Dedup != "mmr" {
		t.Errorf("Dedup: got %q, want %q", f.Dedup, "mmr")
	}
	if f.Level != "l1" {
		t.Errorf("Level: got %q, want %q", f.Level, "l1")
	}
	if f.AgentID != "a1" {
		t.Errorf("AgentID: got %q, want %q", f.AgentID, "a1")
	}
	if f.PrefTopK != 3 {
		t.Errorf("PrefTopK: got %d, want 3", f.PrefTopK)
	}
}

func TestParseSearchCacheKey_MissingFieldsZeroValues(t *testing.T) {
	// Minimal request — all optional fields absent
	body := []byte(`{"user_id":"u","query":"q"}`)
	f, err := ParseSearchCacheKey(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.TopK != 0 {
		t.Errorf("TopK: expected 0 for absent field, got %v", f.TopK)
	}
	if f.Level != "" {
		t.Errorf("Level: expected empty for absent field, got %q", f.Level)
	}
	if f.AgentID != "" {
		t.Errorf("AgentID: expected empty for absent field, got %q", f.AgentID)
	}
	if f.PrefTopK != 0 {
		t.Errorf("PrefTopK: expected 0 for absent field, got %d", f.PrefTopK)
	}
}

func TestParseSearchCacheKey_InvalidJSON(t *testing.T) {
	_, err := ParseSearchCacheKey([]byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestSearchCacheKey_MiddlewareMatchesHandler_Level(t *testing.T) {
	// Regression test: the middleware must generate different keys for the same
	// (user_id, query, top_k, dedup) when level differs (audit P3 bug).
	bodies := [][]byte{
		[]byte(`{"user_id":"u","query":"q","top_k":5,"dedup":"no","level":"l1"}`),
		[]byte(`{"user_id":"u","query":"q","top_k":5,"dedup":"no","level":"l3"}`),
		[]byte(`{"user_id":"u","query":"q","top_k":5,"dedup":"no"}`),
	}
	keys := make([]string, len(bodies))
	for i, b := range bodies {
		f, err := ParseSearchCacheKey(b)
		if err != nil {
			t.Fatalf("ParseSearchCacheKey[%d]: %v", i, err)
		}
		keys[i] = SearchCacheKey(f)
	}
	if keys[0] == keys[1] {
		t.Error("l1 and l3 requests must not share a middleware cache key")
	}
	if keys[0] == keys[2] {
		t.Error("l1 and full-search requests must not share a middleware cache key")
	}
	if keys[1] == keys[2] {
		t.Error("l3 and full-search requests must not share a middleware cache key")
	}
}

func TestPathCacheKey(t *testing.T) {
	key := PathCacheKey("/product/scheduler/allstatus")
	expected := "memdb:cache:path:/product/scheduler/allstatus"
	if key != expected {
		t.Errorf("expected %s, got %s", expected, key)
	}
}
