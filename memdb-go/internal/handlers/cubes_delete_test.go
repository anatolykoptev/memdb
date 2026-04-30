package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// fakeWMCache is a minimal workingMemoryCacher implementation for testing.
// It records VDrop call arguments and can optionally return an error.
type fakeWMCache struct {
	dropCalls  []string // cubeIDs passed to VDrop
	dropErr    error    // non-nil → VDrop returns this error
}

func (f *fakeWMCache) VDrop(_ context.Context, cubeID string) error {
	f.dropCalls = append(f.dropCalls, cubeID)
	return f.dropErr
}

func (f *fakeWMCache) VAdd(_ context.Context, _, _, _ string, _ []float32, _ int64) error {
	return nil
}

func (f *fakeWMCache) VRem(_ context.Context, _, _ string) error { return nil }

func (f *fakeWMCache) VRemBatch(_ context.Context, _ string, _ []string) error { return nil }

func (f *fakeWMCache) VSim(_ context.Context, _ string, _ []float32, _ int) ([]db.VSetCandidate, error) {
	return nil, nil
}

func TestNativeDeleteCube_SoftDefault(t *testing.T) {
	store := &fakeCubeStore{cubes: map[string]db.Cube{
		"target": {CubeID: "target", OwnerID: "alice", IsActive: true},
	}}
	h := newCubeHandler(store)

	payload, _ := json.Marshal(map[string]any{"cube_id": "target", "user_id": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/product/delete_cube", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeDeleteCube(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	if len(store.softDeleted) != 1 || store.softDeleted[0] != "target" {
		t.Errorf("expected soft delete of 'target', got %v", store.softDeleted)
	}
	if len(store.hardDeleted) != 0 {
		t.Errorf("hard delete should NOT be called in default mode")
	}
}

func TestNativeDeleteCube_Hard(t *testing.T) {
	store := &fakeCubeStore{
		cubes:        map[string]db.Cube{"target": {CubeID: "target", OwnerID: "alice", IsActive: true}},
		hardDeletedN: 42,
	}
	h := newCubeHandler(store)

	payload, _ := json.Marshal(map[string]any{"cube_id": "target", "user_id": "alice", "hard_delete": true})
	req := httptest.NewRequest(http.MethodPost, "/product/delete_cube", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeDeleteCube(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	if len(store.hardDeleted) != 1 {
		t.Errorf("expected hard delete, got %v", store.hardDeleted)
	}
	var resp struct {
		Data struct {
			MemoriesDeleted int64 `json:"memories_deleted"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.MemoriesDeleted != 42 {
		t.Errorf("memories_deleted: got %d want 42", resp.Data.MemoriesDeleted)
	}
}

func TestNativeDeleteCube_OwnerMismatch(t *testing.T) {
	store := &fakeCubeStore{cubes: map[string]db.Cube{
		"target": {CubeID: "target", OwnerID: "someone-else", IsActive: true},
	}}
	h := newCubeHandler(store)

	payload, _ := json.Marshal(map[string]any{"cube_id": "target", "user_id": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/product/delete_cube", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeDeleteCube(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d want 403", w.Code)
	}
	if len(store.softDeleted) != 0 || len(store.hardDeleted) != 0 {
		t.Errorf("delete must NOT be called on owner mismatch")
	}
}

// TestNativeDeleteCube_SoftInvalidatesVSET verifies that a successful soft
// delete triggers exactly one VDrop call for the deleted cube.
// Regression test for the stale-VSET bug diagnosed in PR #234:
// Tier 1 returned vset_top=0.9936 on an empty cube because VDrop was never
// called on delete, causing 12/12 near_dup_skip on fresh ingests.
func TestNativeDeleteCube_SoftInvalidatesVSET(t *testing.T) {
	store := &fakeCubeStore{cubes: map[string]db.Cube{
		"target": {CubeID: "target", OwnerID: "alice", IsActive: true},
	}}
	wm := &fakeWMCache{}
	h := newCubeHandler(store)
	h.wmCache = wm

	payload, _ := json.Marshal(map[string]any{"cube_id": "target", "user_id": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/product/delete_cube", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeDeleteCube(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	if len(wm.dropCalls) != 1 {
		t.Errorf("VDrop call count: got %d want 1", len(wm.dropCalls))
	}
	if len(wm.dropCalls) == 1 && wm.dropCalls[0] != "target" {
		t.Errorf("VDrop cube_id: got %q want %q", wm.dropCalls[0], "target")
	}
}

// TestNativeDeleteCube_HardInvalidatesVSET verifies that a successful hard
// delete also calls VDrop exactly once.
func TestNativeDeleteCube_HardInvalidatesVSET(t *testing.T) {
	store := &fakeCubeStore{
		cubes:        map[string]db.Cube{"target": {CubeID: "target", OwnerID: "alice", IsActive: true}},
		hardDeletedN: 7,
	}
	wm := &fakeWMCache{}
	h := newCubeHandler(store)
	h.wmCache = wm

	payload, _ := json.Marshal(map[string]any{"cube_id": "target", "user_id": "alice", "hard_delete": true})
	req := httptest.NewRequest(http.MethodPost, "/product/delete_cube", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeDeleteCube(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	if len(wm.dropCalls) != 1 {
		t.Errorf("VDrop call count: got %d want 1", len(wm.dropCalls))
	}
	if len(wm.dropCalls) == 1 && wm.dropCalls[0] != "target" {
		t.Errorf("VDrop cube_id: got %q want %q", wm.dropCalls[0], "target")
	}
}

// TestNativeDeleteCube_VDROPErrorDoesNotFailRequest verifies that when VDrop
// returns an error the handler still returns 200 (Postgres already committed).
// The cache is recoverable via TTL expiry; failing the client request would be
// doubly harmful (data gone but client retries and gets "not found").
func TestNativeDeleteCube_VDROPErrorDoesNotFailRequest(t *testing.T) {
	store := &fakeCubeStore{cubes: map[string]db.Cube{
		"target": {CubeID: "target", OwnerID: "alice", IsActive: true},
	}}
	wm := &fakeWMCache{dropErr: errors.New("redis: connection refused")}
	h := newCubeHandler(store)
	h.wmCache = wm

	payload, _ := json.Marshal(map[string]any{"cube_id": "target", "user_id": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/product/delete_cube", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeDeleteCube(w, req)

	// Request must succeed — Postgres delete committed, cache is best-effort.
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d want 200 even on VDrop failure", w.Code)
	}
	// VDrop must still have been called (we attempted the invalidation).
	if len(wm.dropCalls) != 1 {
		t.Errorf("VDrop call count: got %d want 1", len(wm.dropCalls))
	}
	// Soft delete must have been recorded.
	if len(store.softDeleted) != 1 {
		t.Errorf("soft delete not recorded: %v", store.softDeleted)
	}
}
