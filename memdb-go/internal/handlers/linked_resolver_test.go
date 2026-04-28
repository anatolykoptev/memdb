// Package handlers — M11 F12 linked_resolver tests.
//
// Covers Resolve happy path, candidate filtering (hallucination guard),
// invalid UUID drop, cap enforcement, empty-input no-op, and mergeLinkedIDs
// dedup/order/cap semantics.
package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// fakeChatServer returns an httptest.Server that responds with the supplied
// JSON content for /v1/chat/completions. The content is wrapped in the
// OpenAI-compatible {choices:[{message:{content}}]} envelope automatically.
func fakeChatServer(t *testing.T, content string) (*httptest.Server, *llm.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	c := llm.NewSimpleClient(srv.URL, "test-key", "test-model")
	return srv, c
}

func TestLinkedIDsResolver_Resolve_TableDriven(t *testing.T) {
	const (
		uuidA = "11111111-1111-1111-1111-111111111111"
		uuidB = "22222222-2222-2222-2222-222222222222"
		uuidC = "33333333-3333-3333-3333-333333333333"
	)

	tests := []struct {
		name       string
		llmContent string
		fact       llm.AtomicFact
		candidates []llm.Candidate
		want       []string
		wantErr    bool
	}{
		{
			name:       "happy path: two valid links survive",
			llmContent: `{"linked_ids":["` + uuidA + `","` + uuidB + `"]}`,
			fact:       llm.AtomicFact{Text: "User adopted a dog named Poppy in March 2025"},
			candidates: []llm.Candidate{
				{ID: uuidA, Memory: "User had a cat named Whiskers"},
				{ID: uuidB, Memory: "User mentioned wanting a dog"},
				{ID: uuidC, Memory: "User likes pizza"},
			},
			want: []string{uuidA, uuidB},
		},
		{
			name:       "hallucination filter: LLM emits UUID not in candidates",
			llmContent: `{"linked_ids":["` + uuidA + `","99999999-9999-9999-9999-999999999999"]}`,
			fact:       llm.AtomicFact{Text: "User said hi"},
			candidates: []llm.Candidate{
				{ID: uuidA, Memory: "User said hello"},
			},
			want: []string{uuidA},
		},
		{
			name:       "invalid UUID dropped",
			llmContent: `{"linked_ids":["not-a-uuid","` + uuidA + `"]}`,
			fact:       llm.AtomicFact{Text: "x"},
			candidates: []llm.Candidate{
				{ID: uuidA, Memory: "y"},
			},
			want: []string{uuidA},
		},
		{
			name:       "empty result: LLM emits empty array",
			llmContent: `{"linked_ids":[]}`,
			fact:       llm.AtomicFact{Text: "x"},
			candidates: []llm.Candidate{{ID: uuidA, Memory: "y"}},
			want:       nil,
		},
		{
			name:       "no fact text: short-circuit nil",
			llmContent: `{"linked_ids":["` + uuidA + `"]}`,
			fact:       llm.AtomicFact{Text: ""},
			candidates: []llm.Candidate{{ID: uuidA, Memory: "y"}},
			want:       nil,
		},
		{
			name:       "no candidates: short-circuit nil",
			llmContent: `{"linked_ids":["` + uuidA + `"]}`,
			fact:       llm.AtomicFact{Text: "x"},
			candidates: nil,
			want:       nil,
		},
		{
			name:       "duplicates collapsed",
			llmContent: `{"linked_ids":["` + uuidA + `","` + uuidA + `","` + uuidB + `"]}`,
			fact:       llm.AtomicFact{Text: "x"},
			candidates: []llm.Candidate{
				{ID: uuidA, Memory: "y"},
				{ID: uuidB, Memory: "z"},
			},
			want: []string{uuidA, uuidB},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, client := fakeChatServer(t, tc.llmContent)
			r := NewLinkedIDsResolver(client, discardLogger())
			got, err := r.Resolve(context.Background(), tc.fact, tc.candidates)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !sliceEq(got, tc.want) {
				t.Fatalf("ids mismatch: got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestLinkedIDsResolver_Resolve_CapEnforced(t *testing.T) {
	// Build linkedResolverMaxLinks+3 valid UUIDs and verify only the first
	// linkedResolverMaxLinks survive.
	const N = 11 // > linkedResolverMaxLinks (8)
	uuids := make([]string, N)
	cands := make([]llm.Candidate, N)
	for i := 0; i < N; i++ {
		// Build a deterministic valid v4-shape UUID per index.
		uuids[i] = newSyntheticUUID(byte(i + 1))
		cands[i] = llm.Candidate{ID: uuids[i], Memory: "memory text"}
	}
	body, _ := json.Marshal(map[string]any{"linked_ids": uuids})
	_, client := fakeChatServer(t, string(body))
	r := NewLinkedIDsResolver(client, discardLogger())
	got, err := r.Resolve(context.Background(), llm.AtomicFact{Text: "x"}, cands)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != linkedResolverMaxLinks {
		t.Fatalf("cap not enforced: got=%d want=%d", len(got), linkedResolverMaxLinks)
	}
	for i, id := range got {
		if id != uuids[i] {
			t.Fatalf("order broken at %d: got=%s want=%s", i, id, uuids[i])
		}
	}
}

func TestLinkedIDsResolver_Resolve_NilClient(t *testing.T) {
	r := NewLinkedIDsResolver(nil, discardLogger())
	_, err := r.Resolve(context.Background(), llm.AtomicFact{Text: "x"},
		[]llm.Candidate{{ID: "11111111-1111-1111-1111-111111111111", Memory: "y"}})
	if err == nil {
		t.Fatalf("expected error from nil client, got nil")
	}
}

func TestMergeLinkedIDs(t *testing.T) {
	const (
		uuidA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		uuidB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		uuidC = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	)
	tests := []struct {
		name     string
		existing []string
		resolved []string
		want     []string
	}{
		{"both empty", nil, nil, nil},
		{"existing only", []string{uuidA}, nil, []string{uuidA}},
		{"resolved only", nil, []string{uuidA}, []string{uuidA}},
		{"existing first preserved", []string{uuidA}, []string{uuidB}, []string{uuidA, uuidB}},
		{"dedup across sets", []string{uuidA, uuidB}, []string{uuidB, uuidC}, []string{uuidA, uuidB, uuidC}},
		{"invalid uuid dropped", []string{"bad", uuidA}, []string{uuidB}, []string{uuidA, uuidB}},
		{"empty string dropped", []string{"", uuidA}, []string{"", uuidB}, []string{uuidA, uuidB}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeLinkedIDs(tc.existing, tc.resolved)
			if !sliceEq(got, tc.want) {
				t.Fatalf("merge mismatch: got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestMergeLinkedIDs_CapEnforced(t *testing.T) {
	const N = 12 // > linkedResolverMaxLinks (8)
	existing := make([]string, N)
	for i := 0; i < N; i++ {
		existing[i] = newSyntheticUUID(byte(i + 1))
	}
	got := mergeLinkedIDs(existing, []string{newSyntheticUUID(0xFF)})
	if len(got) != linkedResolverMaxLinks {
		t.Fatalf("cap not enforced: got=%d want=%d", len(got), linkedResolverMaxLinks)
	}
}

func TestLinkedResolverEnabled_Default(t *testing.T) {
	t.Setenv("MEMDB_F12_LINKED", "")
	if !linkedResolverEnabled() {
		t.Fatal("default should be ON when env is empty")
	}
	t.Setenv("MEMDB_F12_LINKED", "false")
	if linkedResolverEnabled() {
		t.Fatal("should be OFF when env=false")
	}
	t.Setenv("MEMDB_F12_LINKED", "1")
	if !linkedResolverEnabled() {
		t.Fatal("should be ON when env=1")
	}
}

// sliceEq compares two string slices for content equality. Empty == nil.
func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// newSyntheticUUID builds a deterministic 36-char UUID-shaped string keyed
// by a single byte. Suitable for uuid.Parse but not RFC-4122 valid in any
// other sense — we only need valid syntax.
func newSyntheticUUID(b byte) string {
	hex := func(x byte) string {
		const digits = "0123456789abcdef"
		return string([]byte{digits[x>>4], digits[x&0x0F]})
	}
	pair := hex(b)
	// 8-4-4-4-12 = 32 hex chars + 4 hyphens
	return strings.Repeat(pair, 4) + "-" +
		strings.Repeat(pair, 2) + "-" +
		strings.Repeat(pair, 2) + "-" +
		strings.Repeat(pair, 2) + "-" +
		strings.Repeat(pair, 6)
}
