//go:build livepg

// memory_get_by_key_livepg_test.go — end-to-end test for
// POST /product/get_memory_by_key against a live Postgres.
//
// What this exercises:
//  1. Three raw-mode memories ingested with distinct keys.
//  2. NativeGetMemoryByKey returns each memory by its key.
//  3. Unknown key returns 404.
//
// Build tag `livepg` keeps this out of `go test ./...`.
// MEMDB_LIVE_PG_DSN must be set or the test t.Skip's.
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./internal/handlers/... \
//	    -run TestLivePG_GetMemoryByKey -count=1 -v

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestLivePG_GetMemoryByKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg := openLivePGForWindowChars(ctx, t, logger)
	defer pg.Close()

	cube := fmt.Sprintf("livepg-getbykey-%d", time.Now().UnixNano())
	defer cleanupWindowCharsCube(ctx, t, pg, cube)

	h := &Handler{logger: logger, postgres: pg, embedder: &stubEmbedder{}}

	// Insert three raw memories with distinct keys.
	keys := []string{
		"/memories/foo.txt",
		"/memories/bar.txt",
		"/memories/users/alice/prefs.txt",
	}
	for i, k := range keys {
		key := k
		req := &fullAddRequest{
			UserID:          strPtr(cube),
			Mode:            strPtr(modeRaw),
			WritableCubeIDs: []string{cube},
			Key:             &key,
			Messages: []chatMessage{{
				Role:    "user",
				Content: fmt.Sprintf("memory body %d for %s", i, k),
			}},
		}
		if _, err := h.nativeRawAddForCube(ctx, req, cube); err != nil {
			t.Fatalf("ingest key=%s: %v", k, err)
		}
	}

	// Each existing key resolves.
	for _, k := range keys {
		body, _ := json.Marshal(map[string]any{"cube_id": cube, "user_id": cube, "key": k})
		w := httptest.NewRecorder()
		h.NativeGetMemoryByKey(w, httptest.NewRequest(http.MethodPost, "/product/get_memory_by_key", bytes.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("get_memory_by_key key=%s: code=%d body=%s", k, w.Code, w.Body.String())
		}
		var resp struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp.Code != 200 {
			t.Errorf("key=%s: code=%d want 200", k, resp.Code)
		}
		props, _ := resp.Data["properties"].(map[string]any)
		if props == nil {
			t.Fatalf("key=%s: missing properties in %+v", k, resp.Data)
		}
		if got, _ := props["key"].(string); got != k {
			t.Errorf("key roundtrip: want %q got %q", k, got)
		}
	}

	// Missing key → 404.
	body, _ := json.Marshal(map[string]any{"cube_id": cube, "user_id": cube, "key": "/memories/nope.txt"})
	w := httptest.NewRecorder()
	h.NativeGetMemoryByKey(w, httptest.NewRequest(http.MethodPost, "/product/get_memory_by_key", bytes.NewReader(body)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing key: want 404 got %d body=%s", w.Code, w.Body.String())
	}
}
