package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockLLM creates a test HTTP server that returns the given JSON body.
func mockLLM(t *testing.T, responseBody string) (*httptest.Server, *LLMExtractor) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": responseBody}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	ext := NewLLMExtractor(srv.URL, "", "gemini-2.0-flash-lite")
	return srv, ext
}

// --- ExtractAndDedup (v2 unified) tests ---

func TestExtractAndDedup_Basic(t *testing.T) {
	rawLLM := `[
		{"memory":"Alice likes hiking on weekends","type":"UserMemory","action":"add","confidence":0.95},
		{"memory":"The meeting is scheduled for Monday at 10am","type":"LongTermMemory","action":"add","confidence":0.9}
	]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "user: I like hiking. The meeting is Monday 10am.", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}
	if facts[0].Type != "UserMemory" {
		t.Errorf("expected UserMemory, got %s", facts[0].Type)
	}
	if facts[0].Action != MemAdd {
		t.Errorf("expected add, got %s", facts[0].Action)
	}
	if facts[0].Confidence < MinConfidence {
		t.Errorf("confidence %f below threshold", facts[0].Confidence)
	}
}

func TestExtractAndDedup_WithCandidates_Update(t *testing.T) {
	rawLLM := `[{"memory":"Alice likes hiking and trail running","type":"UserMemory","action":"update","confidence":0.92,"target_id":"abc123"}]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	candidates := []Candidate{{ID: "abc123", Memory: "Alice likes hiking"}}
	facts, err := ext.ExtractAndDedup(context.Background(), "user: Alice also likes trail running", candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Action != MemUpdate {
		t.Errorf("expected update, got %s", facts[0].Action)
	}
	if facts[0].TargetID != "abc123" {
		t.Errorf("expected target_id abc123, got %s", facts[0].TargetID)
	}
}

func TestExtractAndDedup_ContradictionDelete(t *testing.T) {
	// Graphiti pattern: contradiction → delete existing memory
	rawLLM := `[
		{"memory":"Alice lives in Berlin","type":"UserMemory","action":"add","confidence":0.95},
		{"memory":"","type":"UserMemory","action":"delete","confidence":0.88,"target_id":"old-nyc-id"}
	]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	candidates := []Candidate{{ID: "old-nyc-id", Memory: "Alice lives in New York"}}
	facts, err := ext.ExtractAndDedup(context.Background(), "user: I moved to Berlin last month.", candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts (add + delete), got %d: %+v", len(facts), facts)
	}
	var hasAdd, hasDelete bool
	for _, f := range facts {
		if f.Action == MemAdd {
			hasAdd = true
		}
		if f.Action == MemDelete && f.TargetID == "old-nyc-id" {
			hasDelete = true
		}
	}
	if !hasAdd {
		t.Error("expected an add action for new location")
	}
	if !hasDelete {
		t.Error("expected a delete action for contradicted memory")
	}
}

func TestExtractAndDedup_LowConfidenceFiltered(t *testing.T) {
	// Facts below MinConfidence should be dropped
	rawLLM := `[
		{"memory":"Alice might like jazz","type":"UserMemory","action":"add","confidence":0.4},
		{"memory":"Alice works at Acme Corp","type":"UserMemory","action":"add","confidence":0.9}
	]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "conversation", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact (low-confidence filtered), got %d: %+v", len(facts), facts)
	}
	if facts[0].Memory != "Alice works at Acme Corp" {
		t.Errorf("wrong fact survived: %s", facts[0].Memory)
	}
}

func TestExtractAndDedup_SkipDropped(t *testing.T) {
	// "skip" entries should be absent from output
	rawLLM := `[
		{"memory":"Alice likes hiking","type":"UserMemory","action":"skip","confidence":0.9},
		{"memory":"Bob is a software engineer","type":"LongTermMemory","action":"add","confidence":0.85}
	]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "conversation", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact (skip dropped), got %d", len(facts))
	}
	if facts[0].Action != MemAdd {
		t.Errorf("expected add, got %s", facts[0].Action)
	}
}

func TestExtractAndDedup_ValidAt(t *testing.T) {
	rawLLM := `[{"memory":"Project deadline is 2026-03-01","type":"LongTermMemory","action":"add","confidence":0.95,"valid_at":"2026-02-18T00:00:00Z"}]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "user: deadline is March 1st", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].ValidAt == "" {
		t.Error("expected valid_at to be set")
	}
}

func TestExtractAndDedup_MarkdownFences(t *testing.T) {
	rawLLM := "```json\n[{\"memory\":\"Bob is 30 years old\",\"type\":\"UserMemory\",\"action\":\"add\",\"confidence\":0.95}]\n```"
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "user: Bob is 30.", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 || facts[0].Memory != "Bob is 30 years old" {
		t.Errorf("unexpected facts: %+v", facts)
	}
}

func TestExtractAndDedup_Empty(t *testing.T) {
	srv, ext := mockLLM(t, "[]")
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "user: hi\nassistant: hello", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("expected empty facts, got %v", facts)
	}
}

func TestExtractAndDedup_InvalidTypeDefaultsToLTM(t *testing.T) {
	rawLLM := `[{"memory":"Project deadline is Friday","type":"UnknownType","action":"add","confidence":0.9}]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "conversation text", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 || facts[0].Type != "LongTermMemory" {
		t.Errorf("expected LongTermMemory fallback, got %+v", facts)
	}
}

// --- ExtractFacts (legacy wrapper) tests ---

func TestExtractFacts_Basic(t *testing.T) {
	rawLLM := `[
		{"memory":"Alice likes hiking on weekends","type":"UserMemory","action":"add","confidence":0.95},
		{"memory":"The meeting is scheduled for Monday at 10am","type":"LongTermMemory","action":"add","confidence":0.9}
	]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractFacts(context.Background(), "user: I like hiking. The meeting is Monday 10am.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}
	if facts[0].Type != "UserMemory" {
		t.Errorf("expected UserMemory, got %s", facts[0].Type)
	}
}

// --- JudgeDedupMerge (legacy compat) tests ---

func TestJudgeDedupMerge_NoCandidates(t *testing.T) {
	srv, ext := mockLLM(t, `[]`)
	defer srv.Close()

	decision, err := ext.JudgeDedupMerge(context.Background(), "new fact", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Action != DedupAdd {
		t.Errorf("expected add, got %s", decision.Action)
	}
}

func TestJudgeDedupMerge_Update(t *testing.T) {
	rawLLM := `[{"memory":"Alice likes hiking and trail running","type":"UserMemory","action":"update","confidence":0.92,"target_id":"abc123"}]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	candidates := []Candidate{{ID: "abc123", Memory: "Alice likes hiking"}}
	decision, err := ext.JudgeDedupMerge(context.Background(), "Alice also likes trail running", candidates)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Action != DedupUpdate {
		t.Errorf("expected update, got %s", decision.Action)
	}
	if decision.TargetID != "abc123" {
		t.Errorf("expected target_id abc123, got %s", decision.TargetID)
	}
	if decision.NewMemory == "" {
		t.Errorf("expected merged new_memory, got empty")
	}
}

func TestJudgeDedupMerge_LLMErrorDefaultsToAdd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ext := NewLLMExtractor(srv.URL, "", "")
	candidates := []Candidate{{ID: "x", Memory: "some fact"}}
	decision, _ := ext.JudgeDedupMerge(context.Background(), "new fact", candidates)
	if decision.Action != DedupAdd {
		t.Errorf("LLM error should default to add, got %s", decision.Action)
	}
}

// --- Entity extraction tests ---

func TestExtractAndDedup_WithEntities(t *testing.T) {
	rawLLM := `[{
		"memory": "Ivan works at Yandex in Moscow",
		"type": "LongTermMemory",
		"action": "add",
		"confidence": 0.95,
		"entities": [
			{"name": "Ivan", "type": "PERSON"},
			{"name": "Yandex", "type": "ORG"},
			{"name": "Moscow", "type": "PLACE"}
		],
		"relations": [
			{"subject": "Ivan", "predicate": "WORKS_AT", "object": "Yandex"},
			{"subject": "Yandex", "predicate": "LOCATED_IN", "object": "Moscow"}
		]
	}]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "user: Ivan works at Yandex in Moscow", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	f := facts[0]

	// Verify entities
	if len(f.Entities) != 3 {
		t.Errorf("expected 3 entities, got %d: %+v", len(f.Entities), f.Entities)
	}
	entityNames := make(map[string]string)
	for _, e := range f.Entities {
		entityNames[e.Name] = e.Type
	}
	if entityNames["Ivan"] != "PERSON" {
		t.Errorf("expected Ivan=PERSON, got %q", entityNames["Ivan"])
	}
	if entityNames["Yandex"] != "ORG" {
		t.Errorf("expected Yandex=ORG, got %q", entityNames["Yandex"])
	}
	if entityNames["Moscow"] != "PLACE" {
		t.Errorf("expected Moscow=PLACE, got %q", entityNames["Moscow"])
	}

	// Verify relations (triplets)
	if len(f.Relations) != 2 {
		t.Errorf("expected 2 relations, got %d: %+v", len(f.Relations), f.Relations)
	}
	rel0 := f.Relations[0]
	if rel0.Subject != "Ivan" || rel0.Predicate != "WORKS_AT" || rel0.Object != "Yandex" {
		t.Errorf("unexpected relation[0]: %+v", rel0)
	}
	rel1 := f.Relations[1]
	if rel1.Subject != "Yandex" || rel1.Predicate != "LOCATED_IN" || rel1.Object != "Moscow" {
		t.Errorf("unexpected relation[1]: %+v", rel1)
	}
}

func TestExtractAndDedup_EntitiesUpToFive(t *testing.T) {
	// Verify that up to 5 entities are parsed (not capped at 3)
	rawLLM := `[{
		"memory": "Alice, Bob, Carol, Dave and Eve met at Google in London",
		"type": "LongTermMemory",
		"action": "add",
		"confidence": 0.9,
		"entities": [
			{"name": "Alice", "type": "PERSON"},
			{"name": "Bob", "type": "PERSON"},
			{"name": "Carol", "type": "PERSON"},
			{"name": "Dave", "type": "PERSON"},
			{"name": "Eve", "type": "PERSON"}
		]
	}]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "user: meeting with 5 people", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if len(facts[0].Entities) != 5 {
		t.Errorf("expected 5 entities (up to 5 limit), got %d", len(facts[0].Entities))
	}
}

func TestExtractAndDedup_EntitiesWithValidAt(t *testing.T) {
	// Verify that valid_at is propagated alongside entities for temporal edge storage
	rawLLM := `[{
		"memory": "Maria joined Acme Corp",
		"type": "LongTermMemory",
		"action": "add",
		"confidence": 0.92,
		"valid_at": "2025-01-15T00:00:00Z",
		"entities": [
			{"name": "Maria", "type": "PERSON"},
			{"name": "Acme Corp", "type": "ORG"}
		],
		"relations": [
			{"subject": "Maria", "predicate": "MEMBER_OF", "object": "Acme Corp"}
		]
	}]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "user: Maria joined Acme Corp in January 2025", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	f := facts[0]
	if f.ValidAt != "2025-01-15T00:00:00Z" {
		t.Errorf("expected valid_at=2025-01-15T00:00:00Z, got %q", f.ValidAt)
	}
	if len(f.Entities) != 2 {
		t.Errorf("expected 2 entities, got %d", len(f.Entities))
	}
	if len(f.Relations) != 1 {
		t.Errorf("expected 1 relation, got %d", len(f.Relations))
	}
	if f.Relations[0].Predicate != "MEMBER_OF" {
		t.Errorf("expected MEMBER_OF predicate, got %q", f.Relations[0].Predicate)
	}
}

func TestExtractAndDedup_NoEntitiesOmitted(t *testing.T) {
	// Facts without entities should parse fine (entities field absent)
	rawLLM := `[{"memory":"The sky is blue","type":"LongTermMemory","action":"add","confidence":0.8}]`
	srv, ext := mockLLM(t, rawLLM)
	defer srv.Close()

	facts, err := ext.ExtractAndDedup(context.Background(), "user: the sky is blue", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if len(facts[0].Entities) != 0 {
		t.Errorf("expected 0 entities, got %d", len(facts[0].Entities))
	}
	if len(facts[0].Relations) != 0 {
		t.Errorf("expected 0 relations, got %d", len(facts[0].Relations))
	}
}

// --- Utility tests ---

func TestStripFences(t *testing.T) {
	cases := []struct{ in, want string }{
		{`["a"]`, `["a"]`},
		{"```json\n[\"a\"]\n```", `["a"]`},
		{"```\n[\"a\"]\n```", `["a"]`},
		{"  [\"a\"]  ", `["a"]`},
	}
	for _, c := range cases {
		got := stripFences(c.in)
		if got != c.want {
			t.Errorf("stripFences(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
