package cache

import "testing"

func TestSearchCacheKey_Valid(t *testing.T) {
	body := []byte(`{"user_id":"memos","query":"test","top_k":5,"dedup":"mmr"}`)
	key := SearchCacheKey(body)

	if key == "" {
		t.Error("expected non-empty cache key")
	}
	if len(key) < 20 {
		t.Error("cache key too short")
	}

	// Same input produces same key
	key2 := SearchCacheKey(body)
	if key != key2 {
		t.Error("same input should produce same key")
	}
}

func TestSearchCacheKey_DifferentInputs(t *testing.T) {
	key1 := SearchCacheKey([]byte(`{"user_id":"memos","query":"test1"}`))
	key2 := SearchCacheKey([]byte(`{"user_id":"memos","query":"test2"}`))

	if key1 == key2 {
		t.Error("different queries should produce different keys")
	}
}

func TestSearchCacheKey_InvalidJSON(t *testing.T) {
	key := SearchCacheKey([]byte(`not json`))
	if key != "" {
		t.Error("invalid JSON should return empty key")
	}
}

func TestPathCacheKey(t *testing.T) {
	key := PathCacheKey("/product/scheduler/allstatus")
	expected := "memdb:cache:path:/product/scheduler/allstatus"
	if key != expected {
		t.Errorf("expected %s, got %s", expected, key)
	}
}
