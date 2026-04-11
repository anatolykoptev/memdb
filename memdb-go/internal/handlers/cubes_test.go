package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func TestNativeCreateCube_Insert(t *testing.T) {
	store := &fakeCubeStore{}
	h := newCubeHandler(store)

	payload, _ := json.Marshal(map[string]any{
		"cube_id": "my-cube", "cube_name": "My Cube", "owner_id": "krolik", "description": "Test cube",
	})
	req := httptest.NewRequest(http.MethodPost, "/product/create_cube", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeCreateCube(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Cube    map[string]any `json:"cube"`
			Created bool           `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Data.Created {
		t.Errorf("created: got false want true")
	}
	if resp.Data.Cube["cube_id"] != "my-cube" {
		t.Errorf("cube_id: got %v", resp.Data.Cube["cube_id"])
	}
	if store.upsertCalls != 1 {
		t.Errorf("upsertCalls: got %d want 1", store.upsertCalls)
	}
}

func TestNativeCreateCube_MissingCubeID(t *testing.T) {
	store := &fakeCubeStore{}
	h := newCubeHandler(store)

	payload, _ := json.Marshal(map[string]any{"owner_id": "krolik"})
	req := httptest.NewRequest(http.MethodPost, "/product/create_cube", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeCreateCube(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", w.Code)
	}
}

func TestNativeListCubes_NoFilter(t *testing.T) {
	store := &fakeCubeStore{cubes: map[string]db.Cube{
		"a": {CubeID: "a", CubeName: "A", OwnerID: "u1", IsActive: true},
		"b": {CubeID: "b", CubeName: "B", OwnerID: "u2", IsActive: true},
		"c": {CubeID: "c", CubeName: "C", OwnerID: "u1", IsActive: false},
	}}
	h := newCubeHandler(store)

	req := httptest.NewRequest(http.MethodPost, "/product/list_cubes", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	h.NativeListCubes(w, req)

	var resp struct {
		Data struct {
			Cubes []map[string]any `json:"cubes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Cubes) != 2 {
		t.Errorf("count: got %d want 2 (inactive must be excluded)", len(resp.Data.Cubes))
	}
}

func TestNativeListCubes_OwnerFilter(t *testing.T) {
	store := &fakeCubeStore{cubes: map[string]db.Cube{
		"a": {CubeID: "a", OwnerID: "u1", IsActive: true},
		"b": {CubeID: "b", OwnerID: "u2", IsActive: true},
	}}
	h := newCubeHandler(store)

	payload, _ := json.Marshal(map[string]any{"owner_id": "u1"})
	req := httptest.NewRequest(http.MethodPost, "/product/list_cubes", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeListCubes(w, req)

	var resp struct {
		Data struct {
			Cubes []map[string]any `json:"cubes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Cubes) != 1 {
		t.Fatalf("count: got %d want 1", len(resp.Data.Cubes))
	}
	if resp.Data.Cubes[0]["cube_id"] != "a" {
		t.Errorf("cube_id: got %v want a", resp.Data.Cubes[0]["cube_id"])
	}
}

func TestNativeGetUserCubes(t *testing.T) {
	store := &fakeCubeStore{cubes: map[string]db.Cube{
		"a": {CubeID: "a", OwnerID: "krolik", IsActive: true},
		"b": {CubeID: "b", OwnerID: "other", IsActive: true},
	}}
	h := newCubeHandler(store)

	payload, _ := json.Marshal(map[string]any{"user_id": "krolik"})
	req := httptest.NewRequest(http.MethodPost, "/product/get_user_cubes", bytes.NewReader(payload))
	w := httptest.NewRecorder()
	h.NativeGetUserCubes(w, req)

	var resp struct {
		Data struct {
			Cubes []map[string]any `json:"cubes"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data.Cubes) != 1 {
		t.Fatalf("count: got %d want 1", len(resp.Data.Cubes))
	}
	if resp.Data.Cubes[0]["cube_id"] != "a" {
		t.Errorf("cube_id: got %v want a", resp.Data.Cubes[0]["cube_id"])
	}
}
