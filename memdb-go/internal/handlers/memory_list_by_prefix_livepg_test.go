//go:build livepg

// memory_list_by_prefix_livepg_test.go — end-to-end test for
// POST /product/list_memories_by_prefix against a live Postgres.
//
// Verifies prefix filtering, key ordering, and pagination using five
// raw-mode memories with overlapping keys.
//
// Build tag `livepg` keeps this out of `go test ./...`.
// MEMDB_LIVE_PG_DSN must be set or the test t.Skip's.

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
	"sort"
	"testing"
	"time"
)

func TestLivePG_ListMemoriesByPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pg := openLivePGForWindowChars(ctx, t, logger)
	defer pg.Close()

	cube := fmt.Sprintf("livepg-listprefix-%d", time.Now().UnixNano())
	defer cleanupWindowCharsCube(ctx, t, pg, cube)

	h := &Handler{logger: logger, postgres: pg, embedder: &stubEmbedder{}}

	keys := []string{
		"/memories/a.txt",
		"/memories/b.txt",
		"/memories/c.txt",
		"/memories/users/alice.txt",
		"/notes/x.txt",
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
				Content: fmt.Sprintf("body %d for %s", i, k),
			}},
		}
		if _, err := h.nativeRawAddForCube(ctx, req, cube); err != nil {
			t.Fatalf("ingest key=%s: %v", k, err)
		}
	}

	doList := func(prefix string, limit, offset int) []map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"cube_id": cube, "user_id": cube, "prefix": prefix,
			"limit": limit, "offset": offset,
		})
		w := httptest.NewRecorder()
		h.NativeListMemoriesByPrefix(w, httptest.NewRequest(http.MethodPost, "/product/list_memories_by_prefix", bytes.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("list prefix=%s: code=%d body=%s", prefix, w.Code, w.Body.String())
		}
		var resp struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp.Data
	}

	// Prefix "/memories/" matches 4 of 5.
	got := doList("/memories/", 100, 0)
	if len(got) != 4 {
		t.Fatalf("prefix /memories/: want 4 rows, got %d (%+v)", len(got), got)
	}
	// Verify ASC ordering.
	gotKeys := make([]string, len(got))
	for i, r := range got {
		gotKeys[i], _ = r["key"].(string)
	}
	sortedKeys := append([]string{}, gotKeys...)
	sort.Strings(sortedKeys)
	for i := range gotKeys {
		if gotKeys[i] != sortedKeys[i] {
			t.Fatalf("rows not sorted ASC by key: %v", gotKeys)
		}
	}
	// char_size must be > 0 for every row.
	for _, r := range got {
		cs, _ := r["char_size"].(float64)
		if cs <= 0 {
			t.Errorf("row %v: char_size=%v want >0", r["key"], cs)
		}
	}

	// Pagination: limit=2, offset=0 then offset=2 must be disjoint and cover the first 4.
	page1 := doList("/memories/", 2, 0)
	page2 := doList("/memories/", 2, 2)
	if len(page1) != 2 || len(page2) != 2 {
		t.Fatalf("pagination: want 2+2 rows, got %d + %d", len(page1), len(page2))
	}
	seen := map[string]bool{}
	for _, r := range append(page1, page2...) {
		k, _ := r["key"].(string)
		if seen[k] {
			t.Errorf("duplicate row across pages: %s", k)
		}
		seen[k] = true
	}

	// Distinct prefix returns 1 row.
	got = doList("/notes/", 100, 0)
	if len(got) != 1 {
		t.Fatalf("prefix /notes/: want 1 row, got %d", len(got))
	}
}
