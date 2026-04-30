package handlers

// add_fast_no_llm_fields_test.go — unit tests for the five no-LLM property
// fields the per-message fast extractor lifts to TOP LEVEL of properties:
//
//   1. attributed_to       — sources[0]["role"]                       (idx_memory_attributed_to)
//   2. event_dates         — regex YYYY-MM-DD + chat_time anchor      (idx_memory_event_dates)
//   3. linked_memory_ids   — forward LTM neighbour in the batch       (idx_memory_linked_ids)
//   4. kind = "fast_msg"   — row discriminator                        (idx_memory_kind)
//   5. per-msg metadata    — chat_time/uuid/agent_id/role into info   (info-key filter pushdown)
//
// All tests stay in the helpers layer (no Postgres required) and reproduce
// the JSONB shape buildFastNodes ships into Postgres so a downstream
// migration / index regression would surface as an assertion failure here.

import (
	"encoding/json"
	"testing"
)

// fakeFastSurvivor builds a fastSurvivor for the helpers-level builder. The
// embedder/postgres surfaces are NOT touched — buildFastNodes only formats
// JSON and constructs MemoryInsertNodes from the survivor + context.
func fakeFastSurvivor(text string, sources []map[string]any, memType string) fastSurvivor {
	return fastSurvivor{
		mem: extractedMemory{
			Text:       text,
			Sources:    sources,
			MemoryType: memType,
		},
		embedding: []float32{0.1, 0.2, 0.3},
		info:      map[string]any{},
		wmID:      "wm-uuid-0000",
		ltmID:     "lt-uuid-0000",
	}
}

func fastTestContext() fastAddContext {
	return fastAddContext{
		cubeID:          "cube-1",
		userID:          "user-1",
		sessionID:       "sess-1",
		now:             "2026-04-29T10:00:00.000000",
		info:            map[string]any{},
		observationDate: "2026-04-29",
	}
}

// unmarshalLTProps fishes the LTM node's JSON properties out of the slice
// buildFastNodes returns ([WM, LTM] order). LTM is the row that matters for
// search-side index correctness — WM is the working twin.
func unmarshalLTProps(t *testing.T, h *Handler, s fastSurvivor, fac fastAddContext, nextLTMID string) map[string]any {
	t.Helper()
	nodes, _, err := h.buildFastNodes(s, fac, nextLTMID)
	if err != nil {
		t.Fatalf("buildFastNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes (WM, LT), got %d", len(nodes))
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(nodes[1].PropertiesJSON), &props); err != nil {
		t.Fatalf("unmarshal LT props: %v", err)
	}
	return props
}

// --- Test 1: attributed_to ---

// TestFastAttributedToIsRole verifies per-message fast rows lift sources[0].role
// to the TOP-LEVEL properties.attributed_to slot — required by migration 0022's
// partial index idx_memory_attributed_to.
func TestFastAttributedToIsRole(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{{
		"role":      "user",
		"content":   "hi there",
		"chat_time": "2026-04-29T10:00:00",
	}}
	s := fakeFastSurvivor("user: [2026-04-29T10:00:00]: hi there", src, memTypeUser)
	props := unmarshalLTProps(t, h, s, fastTestContext(), "")

	got, ok := props["attributed_to"].(string)
	if !ok || got != "user" {
		t.Fatalf("attributed_to = %v (type %T), want \"user\" at top level", props["attributed_to"], props["attributed_to"])
	}
}

// TestFastAttributedToOmittedInWindowMode verifies the partial-index gate:
// window-mode rows (Sources len > 1) skip the lift, so the partial index
// excludes them and the legacy windowed payload stays byte-for-byte stable.
func TestFastAttributedToOmittedInWindowMode(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{
		{"role": "user", "content": "msg 1", "chat_time": "2026-04-29T10:00:00"},
		{"role": "assistant", "content": "msg 2", "chat_time": "2026-04-29T10:00:01"},
	}
	s := fakeFastSurvivor("window text", src, memTypeLongTerm)
	props := unmarshalLTProps(t, h, s, fastTestContext(), "")

	if v, ok := props["attributed_to"]; ok {
		t.Fatalf("attributed_to must be absent for window-mode rows (would corrupt the partial index): got %v", v)
	}
}

// --- Test 2: event_dates ---

// TestFastEventDatesFromText verifies per-message fast rows extract YYYY-MM-DD
// references from message content AND include the chat_time anchor at top
// level — the GIN partial index idx_memory_event_dates only sees rows with
// the property present.
func TestFastEventDatesFromText(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{{
		"role":      "user",
		"content":   "We met on 2024-03-15 and again on 2024-03-22",
		"chat_time": "2026-04-29T10:00:00",
	}}
	s := fakeFastSurvivor("user: [2026-04-29T10:00:00]: We met on 2024-03-15 and again on 2024-03-22", src, memTypeUser)
	props := unmarshalLTProps(t, h, s, fastTestContext(), "")

	raw, ok := props["event_dates"].([]any)
	if !ok {
		t.Fatalf("event_dates = %v (type %T), want []any at top level", props["event_dates"], props["event_dates"])
	}
	got := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		got = append(got, s)
	}
	wantSet := map[string]bool{
		"2024-03-15": false, // body match
		"2024-03-22": false, // body match
		"2026-04-29": false, // chat_time anchor
	}
	for _, d := range got {
		if _, ok := wantSet[d]; !ok {
			t.Errorf("unexpected date %q in event_dates", d)
			continue
		}
		wantSet[d] = true
	}
	for d, seen := range wantSet {
		if !seen {
			t.Errorf("missing %q in event_dates: got %v", d, got)
		}
	}
}

// TestFastEventDatesRejectsImpossibleDate verifies time.Parse validation: the
// regex matches \d{4}-\d{2}-\d{2}, but invalid calendar dates (2025-13-01,
// 2024-02-30) must NOT land in the index.
func TestFastEventDatesRejectsImpossibleDate(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{{
		"role":      "user",
		"content":   "Met on 2025-13-01 and 2024-02-30 — these are not real dates.",
		"chat_time": "2026-04-29T10:00:00",
	}}
	s := fakeFastSurvivor("user: [2026-04-29T10:00:00]: ...", src, memTypeUser)
	props := unmarshalLTProps(t, h, s, fastTestContext(), "")

	raw, ok := props["event_dates"].([]any)
	if !ok {
		// chat_time anchor still keeps event_dates non-empty.
		t.Fatalf("event_dates absent; expected at least the chat_time anchor")
	}
	for _, v := range raw {
		d, _ := v.(string)
		if d == "2025-13-01" || d == "2024-02-30" {
			t.Errorf("impossible date %q passed validation; would poison idx_memory_event_dates", d)
		}
	}
}

// --- Test 3: linked_memory_ids ---

// TestFastLinkedMemoryIDsForwardChain verifies the forward-pointer contract:
// per-message fast rows wire properties.linked_memory_ids = [next survivor's
// LTM ID] so F12's `?|` GIN expand catches the chronological neighbour
// without waiting on the structural-edge writer.
func TestFastLinkedMemoryIDsForwardChain(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{{
		"role":      "user",
		"content":   "first turn",
		"chat_time": "2026-04-29T10:00:00",
	}}
	s := fakeFastSurvivor("user: [2026-04-29T10:00:00]: first turn", src, memTypeUser)
	const nextID = "lt-uuid-NEXT"
	props := unmarshalLTProps(t, h, s, fastTestContext(), nextID)

	raw, ok := props["linked_memory_ids"].([]any)
	if !ok {
		t.Fatalf("linked_memory_ids missing at top level (idx_memory_linked_ids GIN index would never match): props=%#v", props)
	}
	if len(raw) != 1 {
		t.Fatalf("linked_memory_ids = %v, want [%s]", raw, nextID)
	}
	if got, _ := raw[0].(string); got != nextID {
		t.Fatalf("linked_memory_ids[0] = %q, want %q", got, nextID)
	}
}

// TestFastLinkedMemoryIDsTailIsAbsent verifies the chain tail: the last
// survivor in the batch has no forward neighbour and therefore omits
// linked_memory_ids entirely. Empty/nil-array writes would still bloat the
// GIN index (and break any `?| empty` predicate).
func TestFastLinkedMemoryIDsTailIsAbsent(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{{
		"role":      "assistant",
		"content":   "tail message",
		"chat_time": "2026-04-29T10:00:00",
	}}
	s := fakeFastSurvivor("assistant: [2026-04-29T10:00:00]: tail message", src, memTypeLongTerm)
	props := unmarshalLTProps(t, h, s, fastTestContext(), "" /* no forward neighbour */)

	if v, ok := props["linked_memory_ids"]; ok {
		t.Fatalf("linked_memory_ids must be absent on the chain tail; got %v", v)
	}
}

// --- Test 4: kind ---

// TestFastKindIsFastMsg verifies per-message fast rows stamp kind="fast_msg"
// at top level — required by migration 0022's partial index idx_memory_kind
// (predicate WHERE properties ? 'kind'). Without this, search filters like
// kind='fast_msg' silently drop every fast row.
func TestFastKindIsFastMsg(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{{
		"role":      "user",
		"content":   "fact-bearing message",
		"chat_time": "2026-04-29T10:00:00",
	}}
	s := fakeFastSurvivor("user: [2026-04-29T10:00:00]: fact-bearing message", src, memTypeUser)
	props := unmarshalLTProps(t, h, s, fastTestContext(), "")

	got, ok := props["kind"].(string)
	if !ok || got != fastMsgKind {
		t.Fatalf("kind = %v (type %T), want %q at top level", props["kind"], props["kind"], fastMsgKind)
	}

	// Round-trip: simulate the migration's COALESCE(NULLIF(properties->>'kind',''), 'paragraph_legacy').
	// fast rows must resolve to 'fast_msg', NOT 'paragraph_legacy'.
	resolved := got
	if resolved == "" {
		resolved = "paragraph_legacy"
	}
	if resolved != fastMsgKind {
		t.Fatalf("migration would compute kind=%q; expected %q (idx_memory_kind broken)", resolved, fastMsgKind)
	}
}

// TestFastKindOmittedInWindowMode verifies window-mode rows do NOT stamp
// kind=fast_msg — the windowed payload aggregates multiple messages into one
// row and is closer in shape to the legacy paragraph format. Default to the
// migration's COALESCE 'paragraph_legacy' so back-compat search filters work.
func TestFastKindOmittedInWindowMode(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{
		{"role": "user", "content": "msg a", "chat_time": "2026-04-29T10:00:00"},
		{"role": "assistant", "content": "msg b", "chat_time": "2026-04-29T10:00:01"},
	}
	s := fakeFastSurvivor("window text", src, memTypeLongTerm)
	props := unmarshalLTProps(t, h, s, fastTestContext(), "")

	if v, ok := props["kind"]; ok {
		t.Fatalf("kind must be absent for window-mode rows (would force 'fast_msg' on legacy paragraphs): got %v", v)
	}
}

// --- Test 5: per-msg metadata flatten ---

// TestFastSourcesFlattenedToInfo verifies the per-msg metadata flatten
// contract — chat_time / uuid / agent_id / role from sources[0] land in
// properties.info at the TOP LEVEL of the info bag. Mirrors the raw-mode
// shape so info-key search predicates push down to btree/JSONB on both
// paths.
func TestFastSourcesFlattenedToInfo(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{{
		"role":      "user",
		"content":   "hello",
		"chat_time": "2026-04-29T10:00:00",
		"uuid":      "u-msg-1",
		"agent_id":  "ag-test",
	}}
	s := fakeFastSurvivor("user: [2026-04-29T10:00:00]: hello", src, memTypeUser)
	props := unmarshalLTProps(t, h, s, fastTestContext(), "")

	infoRaw, ok := props["info"].(map[string]any)
	if !ok {
		t.Fatalf("info not a map: %v (type %T)", props["info"], props["info"])
	}
	cases := []struct {
		key, want string
	}{
		{"chat_time", "2026-04-29T10:00:00"},
		{"uuid", "u-msg-1"},
		{"agent_id", "ag-test"},
		{"role", "user"},
	}
	for _, c := range cases {
		got, _ := infoRaw[c.key].(string)
		if got != c.want {
			t.Errorf("info[%q] = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestFastSourcesFlattenSkipsWindowMode verifies the gate: window-mode rows
// (Sources len > 1) leave info untouched. Adding chat_time/uuid/agent_id from
// only sources[0] would silently misattribute aggregated rows.
func TestFastSourcesFlattenSkipsWindowMode(t *testing.T) {
	emb := &recordingEmbedder{}
	h := quietHandler(emb)
	src := []map[string]any{
		{"role": "user", "content": "a", "chat_time": "2026-04-29T10:00:00", "uuid": "u-a"},
		{"role": "assistant", "content": "b", "chat_time": "2026-04-29T10:00:01", "uuid": "u-b"},
	}
	s := fakeFastSurvivor("a + b", src, memTypeLongTerm)
	props := unmarshalLTProps(t, h, s, fastTestContext(), "")

	infoRaw, ok := props["info"].(map[string]any)
	if !ok {
		t.Fatalf("info not a map: %v (type %T)", props["info"], props["info"])
	}
	for _, k := range []string{"chat_time", "uuid", "agent_id", "role"} {
		if v, ok := infoRaw[k]; ok {
			t.Errorf("info[%q] = %v; window-mode rows must skip the flatten (would misattribute aggregated row)", k, v)
		}
	}
}
