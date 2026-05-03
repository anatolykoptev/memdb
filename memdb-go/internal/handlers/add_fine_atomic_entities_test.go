package handlers

// add_fine_atomic_entities_test.go — coverage for the fix that promotes
// AtomicFact.NamedEntitiesInText into ExtractedFact.Entities so the
// shared linkEntitiesAsync path (collectHandlerEntityPairs filters on
// len(Entities)>0) writes entity_nodes + MENTIONS_ENTITY edges for
// atomic-extracted facts. Without these tests the regression is silent
// — pre-fix forensic finding for conv-26 was 74 atomic_facts rows but
// 0 entity_nodes rows.

import (
	"reflect"
	"sort"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// TestAtomicToExtracted_PromotesNamedEntities is the happy path: a fact
// with three proper nouns yields three EntityMention rows that
// collectHandlerEntityPairs will surface to the entity-link goroutine.
func TestAtomicToExtracted_PromotesNamedEntities(t *testing.T) {
	in := llm.AtomicFact{
		Text:                "Melanie owns three pets named Oliver, Luna, and Bailey.",
		AttributedTo:        "user",
		NamedEntitiesInText: []string{"Melanie", "Oliver", "Luna", "Bailey"},
	}
	got := atomicToExtracted(in)
	if len(got.Entities) != 4 {
		t.Fatalf("expected 4 entities promoted, got %d (%+v)", len(got.Entities), got.Entities)
	}
	wantNames := []string{"Bailey", "Luna", "Melanie", "Oliver"}
	gotNames := make([]string, 0, len(got.Entities))
	for _, e := range got.Entities {
		gotNames = append(gotNames, e.Name)
		if e.Type != "PERSON" {
			t.Errorf("single capitalized token %q: want PERSON, got %s", e.Name, e.Type)
		}
	}
	sort.Strings(gotNames)
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Errorf("entity names mismatch: got %v want %v", gotNames, wantNames)
	}
}

// TestAtomicToExtracted_EmptyEntities verifies the no-op path. A fact
// with NamedEntitiesInText nil/empty must NOT append to Entities — the
// caller's collectHandlerEntityPairs filter already drops these, but
// emitting empty mentions would break atomic_entities_promoted_total
// accounting (success=0 + failed=0 should mean "no work attempted",
// not "promotion happened with 0 names").
func TestAtomicToExtracted_EmptyEntities(t *testing.T) {
	in := llm.AtomicFact{
		Text:                "User likes coffee.",
		NamedEntitiesInText: nil,
	}
	got := atomicToExtracted(in)
	if got.Entities != nil {
		t.Errorf("nil NamedEntitiesInText must yield nil Entities, got %+v", got.Entities)
	}
	in.NamedEntitiesInText = []string{}
	got = atomicToExtracted(in)
	if got.Entities != nil {
		t.Errorf("empty NamedEntitiesInText must yield nil Entities, got %+v", got.Entities)
	}
}

// TestAtomicToExtracted_DedupesWithinFact protects against the LLM
// repeating a proper noun in NamedEntitiesInText (observed in
// enumeration-heavy chunks). Idempotency is the upsert layer's job, but
// the metric counter would over-report success without dedup here.
func TestAtomicToExtracted_DedupesWithinFact(t *testing.T) {
	in := llm.AtomicFact{
		Text:                "Caroline mentioned Caroline twice.",
		NamedEntitiesInText: []string{"Caroline", "caroline", "  Caroline  ", "CAROLINE"},
	}
	got := atomicToExtracted(in)
	if len(got.Entities) != 1 {
		t.Fatalf("case-insensitive dedup failed: got %d entities (%+v)", len(got.Entities), got.Entities)
	}
}

// TestAtomicToExtracted_QuotedTitleClassifiedAsWORK exercises the
// type-inference heuristic for quoted strings — the dominant cat2 miss
// in pre-fix runs was "Becoming Nicole" being lost entirely from the
// graph. Quote stripping must canonicalise the entity name so the
// stored normalized id matches the unquoted query-time lookup.
func TestAtomicToExtracted_QuotedTitleClassifiedAsWORK(t *testing.T) {
	in := llm.AtomicFact{
		Text:                `Caroline read "Becoming Nicole" last spring.`,
		NamedEntitiesInText: []string{"Caroline", `"Becoming Nicole"`},
	}
	got := atomicToExtracted(in)
	if len(got.Entities) != 2 {
		t.Fatalf("expected 2 entities, got %d (%+v)", len(got.Entities), got.Entities)
	}
	for _, e := range got.Entities {
		switch e.Name {
		case "Caroline":
			if e.Type != "PERSON" {
				t.Errorf("Caroline: want PERSON, got %s", e.Type)
			}
		case "Becoming Nicole":
			if e.Type != "WORK" {
				t.Errorf("quoted title: want WORK, got %s", e.Type)
			}
		default:
			t.Errorf("unexpected entity name %q (quote stripping likely failed)", e.Name)
		}
	}
}

// TestInferEntityType_AllUpperIsORG covers the acronym branch (NASA, IBM).
// Single-letter acronyms fall through to PERSON intentionally — too many
// false positives ("I", initials) outweigh the rare 1-char ORG.
func TestInferEntityType_AllUpperIsORG(t *testing.T) {
	cases := map[string]string{
		"NASA":           "ORG",
		"IBM":            "ORG",
		"F-35":           "ORG", // letters are upper; digits/punct ignored
		"Shopify":        "PERSON",
		"becoming":       "ENTITY",
		"hello world":    "ENTITY",
		"Marcus":         "PERSON",
		`"Hidden Figures"`: "WORK",
	}
	for in, want := range cases {
		got := inferEntityType(in)
		if got != want {
			t.Errorf("inferEntityType(%q): got %s, want %s", in, got, want)
		}
	}
}

// TestStripEntityQuotes covers the canonicalisation step the upsert
// layer relies on (entity_nodes.id = NormalizeEntityID(name)). Smart
// quotes and ASCII quotes both strip; mismatched pairs and bare words
// pass through.
func TestStripEntityQuotes(t *testing.T) {
	cases := map[string]string{
		`"hello"`:      "hello",
		`'world'`:      "world",
		"“smart”":      "smart",
		"«guillemet»":  "guillemet",
		"unquoted":     "unquoted",
		`"mismatched'`: `"mismatched'`, // pairs must match
		`""`:           "",
	}
	for in, want := range cases {
		got := stripEntityQuotes(in)
		if got != want {
			t.Errorf("stripEntityQuotes(%q): got %q, want %q", in, got, want)
		}
	}
}

// TestNamedEntitiesToMentions_FiltersBlanks defends against the LLM
// emitting whitespace-only entries (observed when the prompt is
// retried under low temperature on a sparse chunk).
func TestNamedEntitiesToMentions_FiltersBlanks(t *testing.T) {
	got := namedEntitiesToMentions([]string{"", "  ", "\t", "Marcus"})
	if len(got) != 1 || got[0].Name != "Marcus" {
		t.Errorf("blank filter failed: got %+v", got)
	}
}

// TestCollectHandlerEntityPairs_PicksUpAtomicFacts is the integration
// guard: collectHandlerEntityPairs is the chokepoint that turned the
// atomic path into a no-op pre-fix (len(ef.fact.Entities)==0 → skipped).
// With promotion in place, an embeddedFact built from atomicToExtracted
// must produce one entityLinkPair per fact with all promoted entities
// preserved.
func TestCollectHandlerEntityPairs_PicksUpAtomicFacts(t *testing.T) {
	atomic := llm.AtomicFact{
		Text:                "Melanie's pets are Oliver, Luna, Bailey.",
		NamedEntitiesInText: []string{"Melanie", "Oliver", "Luna", "Bailey"},
	}
	ext := atomicToExtracted(atomic)
	embedded := []embeddedFact{{
		fact:  ext,
		ltmID: "ltm-test-0001",
	}}
	pairs := collectHandlerEntityPairs(embedded)
	if len(pairs) != 1 {
		t.Fatalf("expected 1 entity-link pair from atomic fact, got %d (regression: pre-fix returned 0)", len(pairs))
	}
	if len(pairs[0].entities) != 4 {
		t.Errorf("entity count drift: got %d want 4", len(pairs[0].entities))
	}
	if pairs[0].ltmID != "ltm-test-0001" {
		t.Errorf("ltmID propagation broken: got %q", pairs[0].ltmID)
	}
}
