package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func TestNativeDeleteCube_SoftDefault(t *testing.T) {
	store := &fakeCubeStore{cubes: map[string]db.Cube{
		"target": {CubeID: "target", OwnerID: "krolik", IsActive: true},
	}}
	h := newCubeHandler(store)

	payload, _ := json.Marshal(map[string]any{"cube_id": "target", "user_id": "krolik"})
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
		cubes:        map[string]db.Cube{"target": {CubeID: "target", OwnerID: "krolik", IsActive: true}},
		hardDeletedN: 42,
	}
	h := newCubeHandler(store)

	payload, _ := json.Marshal(map[string]any{"cube_id": "target", "user_id": "krolik", "hard_delete": true})
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

	payload, _ := json.Marshal(map[string]any{"cube_id": "target", "user_id": "krolik"})
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
