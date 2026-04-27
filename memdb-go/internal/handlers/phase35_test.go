package handlers

// phase35_test.go — unit tests for Phase 3.5 features:
//   3.5.1 textHash (content-hash dedup)
//   3.5.3 importance_score / retrieval_count defaults in buildNodeProps
//   3.5.1 content_hash stored in info when buildAddNodes is called with ContentHash

import (
	"encoding/json"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// --- 3.5.1: textHash ---

func TestTextHash_Deterministic(t *testing.T) {
	h1 := textHash("Hello World")
	h2 := textHash("Hello World")
	if h1 != h2 {
		t.Fatalf("textHash is not deterministic: %q != %q", h1, h2)
	}
}

func TestTextHash_CaseNormalized(t *testing.T) {
	h1 := textHash("Hello World")
	h2 := textHash("hello world")
	h3 := textHash("HELLO WORLD")
	if h1 != h2 || h2 != h3 {
		t.Fatalf("textHash does not normalize case: %q %q %q", h1, h2, h3)
	}
}

func TestTextHash_WhitespaceNormalized(t *testing.T) {
	h1 := textHash("  hello world  ")
	h2 := textHash("hello world")
	if h1 != h2 {
		t.Fatalf("textHash does not trim whitespace: %q != %q", h1, h2)
	}
}

func TestTextHash_DifferentTexts(t *testing.T) {
	h1 := textHash("user lives in NYC")
	h2 := textHash("user moved to Berlin")
	if h1 == h2 {
		t.Fatal("textHash should differ for different texts")
	}
}

func TestTextHash_Length(t *testing.T) {
	h := textHash("test")
	if len(h) != 32 {
		t.Fatalf("expected 32 hex chars (16 bytes), got %d: %q", len(h), h)
	}
}

// --- 3.5.3: importance_score and retrieval_count defaults in buildNodeProps ---

func TestBuildNodeProps_ImportanceScoreDefault(t *testing.T) {
	props := buildNodeProps(memoryNodeProps{
		ID:         "test-id",
		Memory:     "test memory",
		MemoryType: "LongTermMemory",
		UserName:   "user1",
		Mode:       "fast",
		Now:        "2026-01-01T00:00:00",
		CreatedAt:  "2026-01-01T00:00:00",
		Info:       map[string]any{},
	})

	score, ok := props["importance_score"]
	if !ok {
		t.Fatal("importance_score missing from buildNodeProps output")
	}
	if score != 1.0 {
		t.Fatalf("expected importance_score=1.0, got %v", score)
	}

	count, ok := props["retrieval_count"]
	if !ok {
		t.Fatal("retrieval_count missing from buildNodeProps output")
	}
	if count != 0 {
		t.Fatalf("expected retrieval_count=0, got %v", count)
	}

	_, ok = props["last_retrieved_at"]
	if !ok {
		t.Fatal("last_retrieved_at missing from buildNodeProps output")
	}
}

// --- 3.5.1: content_hash stored via buildAddNodes ---

func TestBuildAddNodes_ContentHashInInfo(t *testing.T) {
	hash := textHash("user works at Acme Corp")
	fact := llm.ExtractedFact{
		Memory:      "user works at Acme Corp",
		Type:        "LongTermMemory",
		Action:      llm.MemAdd,
		Confidence:  0.95,
		ContentHash: hash,
	}
	embVec := "[0.1,0.2,0.3]"
	nodes, item := buildAddNodes(fact, embVec, nil, "cube1", "user1", "", "sess1", "2026-01-01T00:00:00",
		map[string]any{}, nil, nil, "")

	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes (WM+LTM), got %d", len(nodes))
	}
	if item == nil {
		t.Fatal("expected non-nil item")
	}

	// Verify content_hash is in the LTM node's properties info field.
	ltmNode := nodes[1]
	var props map[string]any
	if err := json.Unmarshal(ltmNode.PropertiesJSON, &props); err != nil {
		t.Fatalf("unmarshal ltm props: %v", err)
	}
	info, _ := props["info"].(map[string]any)
	if info == nil {
		t.Fatal("info field missing or wrong type in LTM node properties")
	}
	storedHash, _ := info["content_hash"].(string)
	if storedHash != hash {
		t.Fatalf("expected content_hash=%q in info, got %q", hash, storedHash)
	}
}

func TestBuildAddNodes_EmptyEmbVecReturnsNil(t *testing.T) {
	fact := llm.ExtractedFact{Memory: "test", Type: "LongTermMemory", Action: llm.MemAdd}
	nodes, item := buildAddNodes(fact, "", nil, "cube1", "user1", "", "sess1", "2026-01-01T00:00:00",
		map[string]any{}, nil, nil, "")
	if nodes != nil || item != nil {
		t.Fatal("expected nil nodes and nil item when embVec is empty")
	}
}

// --- 3.5.1: fast-mode info copy does not mutate shared map ---

func TestBuildMemoryProperties_InfoNotMutated(t *testing.T) {
	sharedInfo := map[string]any{"key": "value"}
	original := sharedInfo["key"]

	// Simulate per-memory info copy as done in nativeFastAddForCube.
	memInfo := make(map[string]any, len(sharedInfo)+1)
	for k, v := range sharedInfo {
		memInfo[k] = v
	}
	memInfo["content_hash"] = "abc123"

	buildMemoryProperties("id1", "text", "LongTermMemory", "user", "user", "", "sess",
		"2026-01-01T00:00:00", memInfo, nil, nil, "")

	if sharedInfo["key"] != original {
		t.Fatal("sharedInfo was mutated by per-memory copy")
	}
	if _, leaked := sharedInfo["content_hash"]; leaked {
		t.Fatal("content_hash leaked into sharedInfo")
	}
}

// --- D6/D8: raw_text + preference_category serialised into properties ---

func TestBuildNodeProps_RawTextAndPreferenceCategory(t *testing.T) {
	props := buildNodeProps(memoryNodeProps{
		ID:                 "pref-1",
		Memory:             "The user is vegetarian",
		MemoryType:         "PreferenceMemory",
		UserName:           "cube1",
		UserID:             "user1",
		Mode:               modeFine,
		Now:                "2026-04-23T00:00:00",
		CreatedAt:          "2026-04-23T00:00:00",
		Info:               map[string]any{},
		RawText:            "I'm vegetarian",
		PreferenceCategory: "food",
	})
	if props["raw_text"] != "I'm vegetarian" {
		t.Errorf("expected raw_text=%q, got %v", "I'm vegetarian", props["raw_text"])
	}
	if props["preference_category"] != "food" {
		t.Errorf("expected preference_category=food, got %v", props["preference_category"])
	}
}

func TestBuildNodeProps_NewFieldsOmittedWhenEmpty(t *testing.T) {
	// Back-compat: callers that don't set RawText / PreferenceCategory must
	// produce properties without those keys (keeps payload lean and
	// non-breaking for downstream JSON consumers).
	props := buildNodeProps(memoryNodeProps{
		ID:         "id1",
		Memory:     "regular fact",
		MemoryType: "LongTermMemory",
		UserName:   "cube1",
		Mode:       modeFast,
		Now:        "2026-04-23T00:00:00",
		CreatedAt:  "2026-04-23T00:00:00",
		Info:       map[string]any{},
	})
	if _, ok := props["raw_text"]; ok {
		t.Error("raw_text must be absent when RawText is empty")
	}
	if _, ok := props["preference_category"]; ok {
		t.Error("preference_category must be absent when PreferenceCategory is empty")
	}
}

func TestBuildAddNodes_PropagatesD6D8Fields(t *testing.T) {
	fact := llm.ExtractedFact{
		Memory:             "The user is vegetarian",
		RawText:            "I'm vegetarian",
		ResolvedText:       "The user is vegetarian",
		Type:               "PreferenceMemory",
		PreferenceCategory: "food",
		Action:             llm.MemAdd,
		Confidence:         0.95,
	}
	embVec := "[0.1,0.2,0.3]"
	nodes, item := buildAddNodes(fact, embVec, nil, "cube1", "user1", "", "sess1",
		"2026-04-23T00:00:00", map[string]any{}, nil, nil, "")
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes (WM+LTM), got %d", len(nodes))
	}
	if item == nil {
		t.Fatal("expected non-nil item")
	}
	for i, n := range nodes {
		var props map[string]any
		if err := json.Unmarshal(n.PropertiesJSON, &props); err != nil {
			t.Fatalf("unmarshal node[%d]: %v", i, err)
		}
		if props["raw_text"] != "I'm vegetarian" {
			t.Errorf("node[%d]: raw_text missing/wrong: %v", i, props["raw_text"])
		}
		if props["preference_category"] != "food" {
			t.Errorf("node[%d]: preference_category missing/wrong: %v", i, props["preference_category"])
		}
	}
}
