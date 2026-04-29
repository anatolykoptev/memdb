package handlers

// dual_speaker_test.go — M9 server-side dual-speaker unit tests.
//
// Covers the pure-logic paths that don't need a live SearchService:
//   - tagSpeakerLabel: speaker_label stamping + non-mutating clone
//   - mergeDualSpeakerResults: interleave / score / dedup / topK cap
//   - buildDualSpeakerPromptBlock: per-speaker block rendering + empty leg
//   - validation: speakers/top_k_per_speaker/merge_strategy bounds
//   - resolveBasePrompt routing (caller prompt > dual block > "")
//   - validateSearchRequired: user_id optional only when len(Speakers)>=2

import (
	"strings"
	"testing"
)

func TestTagSpeakerLabel_StampsAndClones(t *testing.T) {
	original := []map[string]any{
		{"id": "m1", "memory": "alice loves blue", "metadata": map[string]any{"relativity": 0.9}},
	}
	tagged := tagSpeakerLabel(original, "alice")
	if len(tagged) != 1 {
		t.Fatalf("want 1 tagged memory, got %d", len(tagged))
	}
	md, ok := tagged[0]["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing on tagged memory: %#v", tagged[0])
	}
	if got, _ := md["speaker_label"].(string); got != "alice" {
		t.Errorf("speaker_label want 'alice' got %q", got)
	}

	// Non-mutating: original metadata should NOT have speaker_label.
	origMd, _ := original[0]["metadata"].(map[string]any)
	if _, has := origMd["speaker_label"]; has {
		t.Errorf("tagSpeakerLabel mutated input metadata")
	}
}

func TestTagSpeakerLabel_EmptyInput(t *testing.T) {
	if out := tagSpeakerLabel(nil, "x"); out != nil {
		t.Errorf("want nil for empty input, got %#v", out)
	}
}

func TestMergeDualSpeakerResults_InterleavePreservesDiversity(t *testing.T) {
	a := []map[string]any{
		{"id": "a1", "metadata": map[string]any{"relativity": 0.8}},
		{"id": "a2", "metadata": map[string]any{"relativity": 0.7}},
	}
	b := []map[string]any{
		{"id": "b1", "metadata": map[string]any{"relativity": 0.95}},
		{"id": "b2", "metadata": map[string]any{"relativity": 0.6}},
	}
	results := []dualSpeakerSearchResult{
		{speaker: "alice", memories: a},
		{speaker: "bob", memories: b},
	}
	merged := mergeDualSpeakerResults(results, "interleave", 4)
	if len(merged) != 4 {
		t.Fatalf("want 4 merged, got %d", len(merged))
	}
	wantOrder := []string{"a1", "b1", "a2", "b2"}
	for i, m := range merged {
		got, _ := m["id"].(string)
		if got != wantOrder[i] {
			t.Errorf("interleave order[%d]=%q want %q", i, got, wantOrder[i])
		}
	}
}

func TestMergeDualSpeakerResults_ScoreFlatSort(t *testing.T) {
	a := []map[string]any{
		{"id": "a1", "metadata": map[string]any{"relativity": 0.8}},
	}
	b := []map[string]any{
		{"id": "b1", "metadata": map[string]any{"relativity": 0.95}},
	}
	results := []dualSpeakerSearchResult{
		{speaker: "a", memories: a},
		{speaker: "b", memories: b},
	}
	merged := mergeDualSpeakerResults(results, "score", 2)
	if len(merged) != 2 {
		t.Fatalf("want 2 merged, got %d", len(merged))
	}
	if id, _ := merged[0]["id"].(string); id != "b1" {
		t.Errorf("score sort: top-1 should be b1 (rel=0.95), got %q", id)
	}
}

func TestMergeDualSpeakerResults_DedupByID(t *testing.T) {
	dup := map[string]any{"id": "shared", "metadata": map[string]any{"relativity": 0.9}}
	a := []map[string]any{dup, {"id": "a1"}}
	b := []map[string]any{dup, {"id": "b1"}}
	results := []dualSpeakerSearchResult{
		{speaker: "a", memories: a},
		{speaker: "b", memories: b},
	}
	merged := mergeDualSpeakerResults(results, "interleave", 10)
	seen := map[string]int{}
	for _, m := range merged {
		id, _ := m["id"].(string)
		seen[id]++
	}
	if seen["shared"] != 1 {
		t.Errorf("dedup: 'shared' should appear once, got %d", seen["shared"])
	}
}

func TestMergeDualSpeakerResults_TopKCap(t *testing.T) {
	a := []map[string]any{{"id": "a1"}, {"id": "a2"}, {"id": "a3"}}
	b := []map[string]any{{"id": "b1"}, {"id": "b2"}, {"id": "b3"}}
	results := []dualSpeakerSearchResult{
		{speaker: "a", memories: a},
		{speaker: "b", memories: b},
	}
	merged := mergeDualSpeakerResults(results, "interleave", 3)
	if len(merged) != 3 {
		t.Errorf("topK=3 cap not honoured: got %d", len(merged))
	}
}

func TestMergeDualSpeakerResults_DropsFailedLegs(t *testing.T) {
	a := []map[string]any{{"id": "a1"}}
	results := []dualSpeakerSearchResult{
		{speaker: "alice", memories: a},
		{speaker: "bob", err: errFakeFailure{}},
	}
	merged := mergeDualSpeakerResults(results, "interleave", 5)
	if len(merged) != 1 {
		t.Errorf("expected 1 memory from successful leg, got %d", len(merged))
	}
}

type errFakeFailure struct{}

func (errFakeFailure) Error() string { return "fake-failure" }

func TestBuildDualSpeakerPromptBlock_RendersBothBlocks(t *testing.T) {
	legs := []chatDualSpeakerLeg{
		{speaker: "alice", memories: []map[string]any{
			{"memory": "alice loves blue"},
		}},
		{speaker: "bob", memories: []map[string]any{
			{"memory": "bob loves red"},
		}},
	}
	out := buildDualSpeakerPromptBlock(legs)
	if !strings.Contains(out, "## Speaker alice memories:") {
		t.Errorf("missing alice header in block:\n%s", out)
	}
	if !strings.Contains(out, "## Speaker bob memories:") {
		t.Errorf("missing bob header in block:\n%s", out)
	}
	if !strings.Contains(out, "alice loves blue") {
		t.Errorf("missing alice memory:\n%s", out)
	}
	if !strings.Contains(out, "bob loves red") {
		t.Errorf("missing bob memory:\n%s", out)
	}
}

func TestBuildDualSpeakerPromptBlock_EmptyLegRendersPlaceholder(t *testing.T) {
	legs := []chatDualSpeakerLeg{
		{speaker: "alice", memories: []map[string]any{{"memory": "x"}}},
		{speaker: "bob", memories: nil},
	}
	out := buildDualSpeakerPromptBlock(legs)
	if !strings.Contains(out, "(no memories retrieved)") {
		t.Errorf("expected placeholder for empty leg, got:\n%s", out)
	}
}

func TestComposeDualSpeakerSystemPrompt_PrefixesHeader(t *testing.T) {
	legs := []chatDualSpeakerLeg{
		{speaker: "alice", memories: []map[string]any{{"memory": "x"}}},
	}
	out := composeDualSpeakerSystemPrompt(legs)
	if !strings.HasPrefix(out, dualSpeakerChatPromptHeader) {
		t.Errorf("composed prompt should start with header, got:\n%s", out)
	}
}

func TestValidateSearchRequest_DualSpeakerOptionalUserID(t *testing.T) {
	q := "what do they like?"
	req := searchRequest{
		Query:    &q,
		Speakers: []string{"alice", "bob"},
	}
	errs := validateSearchRequest(req)
	for _, e := range errs {
		if strings.Contains(e, "user_id is required") {
			t.Errorf("dual-speaker request should NOT require user_id; got error: %q", e)
		}
	}
}

func TestValidateSearchRequest_SingleSpeakerStillRequiresUserID(t *testing.T) {
	q := "x"
	req := searchRequest{Query: &q}
	errs := validateSearchRequest(req)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "user_id is required") {
			found = true
		}
	}
	if !found {
		t.Errorf("legacy single-speaker request must still flag missing user_id")
	}
}

func TestValidateSearchRequest_RejectsEmptySpeaker(t *testing.T) {
	q := "x"
	req := searchRequest{
		Query:    &q,
		Speakers: []string{"alice", ""},
	}
	errs := validateSearchRequest(req)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "speakers") && strings.Contains(e, "non-empty") {
			found = true
		}
	}
	if !found {
		t.Errorf("validate should reject empty speaker entry; errs=%v", errs)
	}
}

func TestValidateSearchRequest_RejectsBadMergeStrategy(t *testing.T) {
	q := "x"
	uid := "u1"
	bad := "bogus"
	req := searchRequest{
		Query:         &q,
		UserID:        &uid,
		MergeStrategy: &bad,
	}
	errs := validateSearchRequest(req)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "merge_strategy") {
			found = true
		}
	}
	if !found {
		t.Errorf("validate should reject merge_strategy='bogus'; errs=%v", errs)
	}
}

func TestValidateSearchRequest_RejectsNegativeTopKPerSpeaker(t *testing.T) {
	q := "x"
	uid := "u1"
	zero := 0
	req := searchRequest{
		Query:          &q,
		UserID:         &uid,
		TopKPerSpeaker: &zero,
	}
	errs := validateSearchRequest(req)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "top_k_per_speaker") {
			found = true
		}
	}
	if !found {
		t.Errorf("validate should reject top_k_per_speaker=0; errs=%v", errs)
	}
}

func TestResolveBasePrompt_CallerPromptWins(t *testing.T) {
	custom := "caller-supplied-prompt"
	req := &nativeChatRequest{SystemPrompt: &custom}
	legs := []chatDualSpeakerLeg{{speaker: "a", memories: []map[string]any{{"memory": "x"}}}}
	h := &Handler{}
	got := h.resolveBasePrompt(req, legs)
	if got != custom {
		t.Errorf("caller SystemPrompt must win; got %q", got)
	}
}

func TestResolveBasePrompt_DualLegsBuildBlock(t *testing.T) {
	req := &nativeChatRequest{}
	legs := []chatDualSpeakerLeg{{speaker: "a", memories: []map[string]any{{"memory": "x"}}}}
	h := &Handler{}
	got := h.resolveBasePrompt(req, legs)
	if !strings.Contains(got, "## Speaker a memories:") {
		t.Errorf("expected dual-speaker block; got:\n%s", got)
	}
}

func TestResolveBasePrompt_NoSpeakersNoCustomReturnsEmpty(t *testing.T) {
	req := &nativeChatRequest{}
	h := &Handler{}
	if got := h.resolveBasePrompt(req, nil); got != "" {
		t.Errorf("legacy single-speaker without SystemPrompt must return empty; got %q", got)
	}
}

func TestValidateChatRequest_DualSpeakerOptionalUserID(t *testing.T) {
	q := "x"
	req := &nativeChatRequest{
		Query:    &q,
		Speakers: []string{"alice", "bob"},
	}
	errs := validateChatRequest(req)
	for _, e := range errs {
		if strings.Contains(e, "user_id is required") {
			t.Errorf("dual-speaker chat req should not require user_id; got: %q", e)
		}
	}
}

func TestValidateChatRequest_SingleSpeakerRequiresUserID(t *testing.T) {
	q := "x"
	req := &nativeChatRequest{Query: &q}
	errs := validateChatRequest(req)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "user_id is required") {
			found = true
		}
	}
	if !found {
		t.Errorf("legacy chat req must still flag missing user_id; errs=%v", errs)
	}
}

func TestChatProfileUserID(t *testing.T) {
	uid := "alice"
	if got := chatProfileUserID(&nativeChatRequest{UserID: &uid}); got != "alice" {
		t.Errorf("want alice got %q", got)
	}
	if got := chatProfileUserID(&nativeChatRequest{}); got != "" {
		t.Errorf("missing UserID must return empty (skip profile injection); got %q", got)
	}
}
