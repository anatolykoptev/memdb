package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"fmt"
	"testing"
)

// ceCacheRecorder records calls to ClearCEScoresTopK and ClearCEScoresTopKForNeighbor.
// It also satisfies the memoryUpdater interface via embedded stubMemoryUpdater.
type ceCacheRecorder struct {
	clearTopKCalls         []string
	clearTopKNeighborCalls []string
	updateErr              error
}

func (r *ceCacheRecorder) UpdateMemoryByID(_ context.Context, _, _ string, _ []byte, _ string) error {
	return r.updateErr
}

func (r *ceCacheRecorder) ClearCEScoresTopK(_ context.Context, memoryID string) error {
	r.clearTopKCalls = append(r.clearTopKCalls, memoryID)
	return nil
}

func (r *ceCacheRecorder) ClearCEScoresTopKForNeighbor(_ context.Context, neighborID string) error {
	r.clearTopKNeighborCalls = append(r.clearTopKNeighborCalls, neighborID)
	return nil
}

// TestNativeUpdateMemory_ClearsCECacheAfterUpdate asserts that both
// ClearCEScoresTopK and ClearCEScoresTopKForNeighbor are called with the
// updated memory's ID after a successful content change.
// RED: this test MUST fail before the implementation is added.
func TestNativeUpdateMemory_ClearsCECacheAfterUpdate(t *testing.T) {
	const memID = "mem-abc-123"
	const cubeID = "user@example.com"

	recorder := &ceCacheRecorder{}

	h := testValidateHandler()
	h.embedder = &stubEmbedder{}
	setPostgresNonNil(h)
	h.memUpdaterField = recorder // intercepts UpdateMemoryByID

	payload, _ := json.Marshal(map[string]any{
		"memory_id": memID,
		"user_id":   cubeID,
		"text":      "updated content",
	})
	req := httptest.NewRequest(http.MethodPost, "/product/update_memory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.NativeUpdateMemory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	if len(recorder.clearTopKCalls) == 0 {
		t.Error("ClearCEScoresTopK was NOT called after content update (stale CE cache bug)")
	} else if recorder.clearTopKCalls[0] != memID {
		t.Errorf("ClearCEScoresTopK called with %q, want %q", recorder.clearTopKCalls[0], memID)
	}

	if len(recorder.clearTopKNeighborCalls) == 0 {
		t.Error("ClearCEScoresTopKForNeighbor was NOT called after content update (stale neighbor CE cache bug)")
	} else if recorder.clearTopKNeighborCalls[0] != memID {
		t.Errorf("ClearCEScoresTopKForNeighbor called with %q, want %q", recorder.clearTopKNeighborCalls[0], memID)
	}
}

// TestNativeUpdateMemory_CECacheClearIsSkippedOnUpdateFailure asserts that
// CE cache clear is NOT attempted when the underlying DB update fails.
func TestNativeUpdateMemory_CECacheClearIsSkippedOnUpdateFailure(t *testing.T) {
	recorder := &ceCacheRecorder{updateErr: ErrTestUpdateFailure}

	h := testValidateHandler()
	h.embedder = &stubEmbedder{}
	setPostgresNonNil(h)
	h.memUpdaterField = recorder

	payload, _ := json.Marshal(map[string]any{
		"memory_id": "mem-xyz",
		"user_id":   "user@example.com",
		"text":      "some text",
	})
	req := httptest.NewRequest(http.MethodPost, "/product/update_memory", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.NativeUpdateMemory(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("expected non-200 on update failure")
	}
	if len(recorder.clearTopKCalls) != 0 {
		t.Error("ClearCEScoresTopK should NOT be called when update fails")
	}
	if len(recorder.clearTopKNeighborCalls) != 0 {
		t.Error("ClearCEScoresTopKForNeighbor should NOT be called when update fails")
	}
}

// ErrTestUpdateFailure is a sentinel error for testing the DB update failure path.
var ErrTestUpdateFailure = fmt.Errorf("simulated DB update failure")
