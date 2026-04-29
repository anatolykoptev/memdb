package handlers

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// TestAtomicFactsEnabled covers the env-flag toggle. Default off; common
// truthy values flip it on.
func TestAtomicFactsEnabled(t *testing.T) {
	t.Setenv(atomicFactsEnvVar, "")
	if atomicFactsEnabled() {
		t.Errorf("default should be off")
	}
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(atomicFactsEnvVar, v)
		if !atomicFactsEnabled() {
			t.Errorf("value %q should enable atomic facts", v)
		}
	}
	for _, v := range []string{"0", "false", "no", "off", "garbage"} {
		t.Setenv(atomicFactsEnvVar, v)
		if atomicFactsEnabled() {
			t.Errorf("value %q should NOT enable atomic facts", v)
		}
	}
	_ = os.Unsetenv(atomicFactsEnvVar)
}

// TestAtomicToExtracted verifies the AtomicFact → ExtractedFact mapping
// preserves Memory text, sets the 'add' action, and stamps the synthetic
// confidence above MinConfidence.
func TestAtomicToExtracted(t *testing.T) {
	in := llm.AtomicFact{
		Text:            "User Marcus moved from Vancouver to Toronto in March 2025 for a Senior Engineer role at Shopify.",
		AttributedTo:    "user",
		LinkedMemoryIDs: []string{"a1b2c3d4-0000-0000-0000-111111111111"},
		TopicTag:        "career",
	}
	got := atomicToExtracted(in)
	if got.Memory != in.Text {
		t.Errorf("Memory mismatch: got %q want %q", got.Memory, in.Text)
	}
	if got.Action != llm.MemAdd {
		t.Errorf("expected MemAdd, got %s", got.Action)
	}
	if got.Confidence < llm.MinConfidence {
		t.Errorf("confidence %f below MinConfidence %f", got.Confidence, llm.MinConfidence)
	}
	if got.Type != "LongTermMemory" {
		t.Errorf("expected LongTermMemory, got %s", got.Type)
	}
	// Canonical "user" / "assistant" roles now also emit a speaker:* tag
	// so downstream filters can scope by speaker uniformly.
	wantTags := []string{atomicFactKind, "career", "speaker:user"}
	if !reflect.DeepEqual(got.Tags, wantTags) {
		t.Errorf("tags mismatch: got %v want %v", got.Tags, wantTags)
	}
}

// TestAtomicInfoFromFact_PopulatesAllFields covers the Info-bag construction
// — kind=atomic_fact, attributed_to, linked_memory_ids, event_dates all
// land in the JSONB that buildNodeProps eventually marshals into properties.
func TestAtomicInfoFromFact_PopulatesAllFields(t *testing.T) {
	in := llm.AtomicFact{
		Text:            "irrelevant for this test",
		AttributedTo:    "Maria",
		LinkedMemoryIDs: []string{"a1b2c3d4-0000-0000-0000-111111111111", "b2c3d4e5-0000-0000-0000-222222222222"},
		EventDates:      []string{"2025-03-14"},
	}
	info := atomicInfoFromFact(in)
	if info["kind"] != atomicFactKind {
		t.Errorf("expected kind=%s, got %v", atomicFactKind, info["kind"])
	}
	if info["attributed_to"] != "Maria" {
		t.Errorf("attributed_to: got %v", info["attributed_to"])
	}
	ids, ok := info["linked_memory_ids"].([]any)
	if !ok || len(ids) != 2 {
		t.Errorf("linked_memory_ids: got %#v", info["linked_memory_ids"])
	}
	dates, ok := info["event_dates"].([]any)
	if !ok || len(dates) != 1 {
		t.Errorf("event_dates: got %#v", info["event_dates"])
	}
	// Round-trip through JSON to confirm pgx jsonb marshal will succeed.
	if _, err := json.Marshal(info); err != nil {
		t.Errorf("json.Marshal failed: %v", err)
	}
}

// TestAtomicInfoFromFact_OmitsEmptyFields keeps the JSONB compact for
// facts that have no speaker / no links / no dates.
func TestAtomicInfoFromFact_OmitsEmptyFields(t *testing.T) {
	info := atomicInfoFromFact(llm.AtomicFact{Text: "x"})
	if _, ok := info["attributed_to"]; ok {
		t.Errorf("attributed_to should be absent")
	}
	if _, ok := info["linked_memory_ids"]; ok {
		t.Errorf("linked_memory_ids should be absent")
	}
	if _, ok := info["event_dates"]; ok {
		t.Errorf("event_dates should be absent")
	}
	if info["kind"] != atomicFactKind {
		t.Errorf("kind always present")
	}
}

// TestAtomicProperties_KindAtTopLevel pins the production add path's
// JSONB shape: kind / linked_memory_ids / attributed_to MUST live at the
// TOP LEVEL of the properties map, not nested under `info`. This is a
// hard invariant tied to migration 0022:
//
//   - the generated `kind` column reads properties->>'kind' (top-level),
//     so info.kind silently degrades every atomic row to 'paragraph_legacy';
//   - idx_memory_linked_ids is a GIN index on (properties -> 'linked_memory_ids')
//     — the F12 soft-edge wiring depends on it;
//   - idx_memory_attributed_to is a partial index on
//     (properties->>'attributed_to').
//
// The pre-existing TestAtomicAndLegacyCoexistInProperties only checked
// info-level structure and missed this entire class of bug. Keep both —
// they pin complementary invariants.
func TestAtomicProperties_KindAtTopLevel(t *testing.T) {
	fact := llm.AtomicFact{
		Text:            "User Marcus moved from Vancouver to Toronto in March 2025 for a Senior Engineer role at Shopify.",
		AttributedTo:    "Marcus",
		LinkedMemoryIDs: []string{"a1b2c3d4-0000-0000-0000-111111111111"},
	}
	factInfo := atomicInfoFromFact(fact)
	props := buildNodeProps(memoryNodeProps{
		ID:         "11111111-1111-1111-1111-111111111111",
		Memory:     fact.Text,
		MemoryType: "LongTermMemory",
		UserName:   "test_cube",
		Mode:       modeFine,
		Now:        "2026-04-27T00:00:00Z",
		CreatedAt:  "2026-04-27T00:00:00Z",
		Info:       factInfo,
	})
	// Mirror what applyAtomicAndPersist does after buildNodeProps.
	liftAtomicDiscriminators(props, factInfo)

	// Invariant 1: kind must be top-level (the generated column source).
	if got, ok := props["kind"].(string); !ok || got != atomicFactKind {
		t.Fatalf("kind missing at top level (would degrade to paragraph_legacy): props=%#v", props)
	}

	// Invariant 2: linked_memory_ids at top-level for the GIN index.
	if _, ok := props["linked_memory_ids"]; !ok {
		t.Fatalf("linked_memory_ids missing at top level (idx_memory_linked_ids GIN index would never match)")
	}

	// Invariant 3: attributed_to at top-level for the partial index.
	if got, ok := props["attributed_to"].(string); !ok || got != "Marcus" {
		t.Fatalf("attributed_to missing at top level (idx_memory_attributed_to partial index would skip): got=%v", props["attributed_to"])
	}

	// Invariant 4: simulate what the migration's GENERATED column would
	// compute. COALESCE(NULLIF(properties->>'kind',''), 'paragraph_legacy').
	// Atomic rows must resolve to 'atomic_fact', NOT 'paragraph_legacy'.
	rawKind, _ := props["kind"].(string)
	resolved := rawKind
	if resolved == "" {
		resolved = "paragraph_legacy"
	}
	if resolved != atomicFactKind {
		t.Fatalf("migration would compute kind=%q; expected %q (F8 / F12 broken)", resolved, atomicFactKind)
	}

	// JSONB round-trip: the marshalled blob's top-level keys are what
	// PostgreSQL sees. Confirm `kind` survives at the top.
	blob, err := json.Marshal(props)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(blob, &roundTrip); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if roundTrip["kind"] != atomicFactKind {
		t.Fatalf("kind lost in JSON round-trip: %v", roundTrip["kind"])
	}
}

// TestLiftAtomicDiscriminators_NoOpOnNonAtomic ensures the helper is safe to
// call against legacy rows (no kind/linked/attributed in factInfo) — must
// not invent keys that would flip the row to atomic_fact.
func TestLiftAtomicDiscriminators_NoOpOnNonAtomic(t *testing.T) {
	props := map[string]any{"id": "x"}
	liftAtomicDiscriminators(props, map[string]any{"unrelated": "value"})
	if _, ok := props["kind"]; ok {
		t.Fatalf("liftAtomicDiscriminators must not invent kind on non-atomic rows")
	}
	if _, ok := props["linked_memory_ids"]; ok {
		t.Fatalf("liftAtomicDiscriminators must not invent linked_memory_ids")
	}
	if _, ok := props["attributed_to"]; ok {
		t.Fatalf("liftAtomicDiscriminators must not invent attributed_to")
	}
}

// TestAtomicAndLegacyCoexistInProperties simulates the F8 acceptance #5
// requirement at the JSONB level: a row with kind=atomic_fact and a row
// without it produce distinct values for the generated `kind` column. We
// don't run a livepg test here (requires the krolik-postgres-age image);
// instead we verify the property builder hands distinct shapes to the DB
// layer, which the migration's COALESCE expression resolves to the right
// kind values at query time.
func TestAtomicAndLegacyCoexistInProperties(t *testing.T) {
	atomicProps := buildNodeProps(memoryNodeProps{
		ID:         "11111111-1111-1111-1111-111111111111",
		Memory:     "Atomic fact memory",
		MemoryType: "LongTermMemory",
		UserName:   "test_cube",
		Mode:       modeFine,
		Now:        "2026-04-27T00:00:00Z",
		CreatedAt:  "2026-04-27T00:00:00Z",
		Info:       atomicInfoFromFact(llm.AtomicFact{Text: "x", AttributedTo: "user"}),
	})
	legacyProps := buildNodeProps(memoryNodeProps{
		ID:         "22222222-2222-2222-2222-222222222222",
		Memory:     "Legacy paragraph memory",
		MemoryType: "LongTermMemory",
		UserName:   "test_cube",
		Mode:       modeFine,
		Now:        "2026-04-27T00:00:00Z",
		CreatedAt:  "2026-04-27T00:00:00Z",
	})

	atomicInfo, _ := atomicProps["info"].(map[string]any)
	if atomicInfo["kind"] != atomicFactKind {
		t.Errorf("atomic row should have info.kind=atomic_fact, got %v", atomicInfo["kind"])
	}
	legacyInfo, _ := legacyProps["info"].(map[string]any)
	if legacyInfo != nil {
		if _, ok := legacyInfo["kind"]; ok {
			t.Errorf("legacy row should NOT carry info.kind (defaults to paragraph_legacy via migration)")
		}
	}
}
