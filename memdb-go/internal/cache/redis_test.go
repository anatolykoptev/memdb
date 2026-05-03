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

func TestSearchCacheKey_V3Prefix(t *testing.T) {
	key := SearchCacheKey(searchKeyFields{UserID: "u", Query: "q"})
	const prefix = "memdb:cache:search:v3:"
	if len(key) < len(prefix) || key[:len(prefix)] != prefix {
		t.Errorf("expected key to start with %q, got %q", prefix, key)
	}
}

// V3 added 13 new fields. Each must affect the resulting hash — otherwise
// callers differing only in (say) include_embedding or internet_search
// would silently collide and receive wrong-shaped cached responses.
func TestSearchCacheKey_V3FieldsAffectKey(t *testing.T) {
	base := searchKeyFields{UserID: "u", Query: "q"}
	baseKey := SearchCacheKey(base)
	cases := []struct {
		name   string
		mutate func(*searchKeyFields)
	}{
		{"Mode", func(f *searchKeyFields) { f.Mode = "fine" }},
		{"NumStages", func(f *searchKeyFields) { f.NumStages = 3 }},
		{"LLMRerank", func(f *searchKeyFields) { f.LLMRerank = true }},
		{"IncludeEmbedding", func(f *searchKeyFields) { f.IncludeEmbedding = true }},
		{"Profile", func(f *searchKeyFields) { f.Profile = "deep" }},
		{"Relativity", func(f *searchKeyFields) { f.Relativity = 0.5 }},
		{"ToolMemTopK", func(f *searchKeyFields) { f.ToolMemTopK = 7 }},
		{"SkillMemTopK", func(f *searchKeyFields) { f.SkillMemTopK = 7 }},
		{"IncludeSkillMem", func(f *searchKeyFields) { f.IncludeSkillMem = true }},
		{"IncludePref", func(f *searchKeyFields) { f.IncludePref = true }},
		{"SearchToolMem", func(f *searchKeyFields) { f.SearchToolMem = true }},
		{"AttributedTo", func(f *searchKeyFields) { f.AttributedTo = "alice" }},
		{"ReadableCubes", func(f *searchKeyFields) { f.ReadableCubes = "cube-a,cube-b" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mod := base
			tc.mutate(&mod)
			k := SearchCacheKey(mod)
			if k == baseKey {
				t.Errorf("%s: key did not change after mutation (collision)", tc.name)
			}
		})
	}
}

// readable_cube_ids order in the request must NOT affect the cache key —
// otherwise [a,b] and [b,a] miss each other despite returning identical results.
func TestParseSearchCacheKey_ReadableCubesSorted(t *testing.T) {
	body1 := []byte(`{"user_id":"u","query":"q","readable_cube_ids":["cube-b","cube-a"]}`)
	body2 := []byte(`{"user_id":"u","query":"q","readable_cube_ids":["cube-a","cube-b"]}`)
	f1, _ := ParseSearchCacheKey(body1)
	f2, _ := ParseSearchCacheKey(body2)
	if SearchCacheKey(f1) != SearchCacheKey(f2) {
		t.Errorf("cube id order changed key — must be sorted: %s vs %s", SearchCacheKey(f1), SearchCacheKey(f2))
	}
}

// internet_search and num_stages parse correctly into v3 fields (regression
// against the v2 parser which silently dropped these).
func TestParseSearchCacheKey_V3FieldsParsed(t *testing.T) {
	body := []byte(`{"user_id":"u","query":"q","mode":"fine","num_stages":3,"llm_rerank":true,"include_embedding":true,"profile":"p1","relativity":0.4,"tool_mem_top_k":2,"skill_mem_top_k":3,"include_skill_memory":true,"include_preference":true,"search_tool_memory":true,"attributed_to":"alice","readable_cube_ids":["c2","c1"]}`)
	f, err := ParseSearchCacheKey(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Mode != "fine" || f.NumStages != 3 || !f.LLMRerank || !f.IncludeEmbedding {
		t.Errorf("core v3 fields not parsed: %+v", f)
	}
	if f.ReadableCubes != "c1,c2" {
		t.Errorf("ReadableCubes expected sorted csv, got %q", f.ReadableCubes)
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
