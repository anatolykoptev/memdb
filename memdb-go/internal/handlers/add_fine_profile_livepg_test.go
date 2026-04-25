//go:build livepg

// Package handlers — add_fine_profile_livepg_test.go: end-to-end test for
// the M10 Stream 2 profile extractor against a live Postgres + stub LLM.
//
// Positive case: ingest a biographical conversation, stub LLM returns the
// Memobase-format markdown TSV, wait for the fire-and-forget goroutine,
// then assert ≥3 rows landed in memos_graph.user_profiles with the
// expected (topic, sub_topic) tuples (basic_info/name, work/company,
// interest/movie).
// Negative case: ingest pure code conversation, stub LLM returns an empty
// list, assert ≤1 user_profiles row for the cube — no false positives.
//
// Build tag `livepg` keeps this out of `go test ./...`.
// MEMDB_LIVE_PG_DSN must be set or the tests t.Skip.
//
// Invocation:
//
//	MEMDB_LIVE_PG_DSN=postgres://memos:<pass>@127.0.0.1:5432/memos?sslmode=disable \
//	GOWORK=off go test -tags=livepg ./memdb-go/internal/handlers/... \
//	    -run TestLivePG_FineAdd_ProfileExtract -count=1 -v

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// stubProfileLLMServer returns an httptest.Server that:
//   - returns a fact-extraction JSON array (single benign fact) for any
//     call whose user message does NOT contain the Memobase profile prompt
//     marker `#### Memo`
//   - returns the supplied profile body (markdown TSV or anything else) for
//     calls that DO contain `#### Memo` — i.e. the profile extractor.
//
// callCounts (extract, profile) lets the test assert exactly one of each.
func stubProfileLLMServer(t *testing.T, profileBody string, callCounts *[2]int64) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var userMsg string
		for _, m := range payload.Messages {
			if m.Role == "user" {
				userMsg = m.Content
			}
		}

		mu.Lock()
		var content string
		if strings.Contains(userMsg, "#### Memo") {
			atomic.AddInt64(&callCounts[1], 1)
			content = profileBody
		} else {
			atomic.AddInt64(&callCounts[0], 1)
			// Minimal non-empty fact extraction so nativeFineAddForCube
			// finishes Step 3 successfully and we reach the profile hook.
			content = `[{
				"reasoning": "stub",
				"raw_text": "stub raw",
				"resolved_text": "The user mentioned biographical info.",
				"memory": "The user mentioned biographical info.",
				"type": "UserMemory",
				"action": "add",
				"confidence": 0.95,
				"tags": ["bio"]
			}]`
		}
		mu.Unlock()

		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": content}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func runProfileExtractScenario(t *testing.T, cubePrefix, profileBody string, messages []chatMessage) (cube, userID string, profileCalls int64, pgOut *db.Postgres) {
	t.Helper()
	t.Setenv("MEMDB_PROFILE_EXTRACT", "true")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pg := openLivePGForWindowChars(ctx, t, logger)
	// Pool stays open until t.Cleanup so test-level assertions can re-query it.
	t.Cleanup(func() { pg.Close() })

	cube = fmt.Sprintf("%s-%d", cubePrefix, time.Now().UnixNano())
	// Cleanup runs at test end (not scenario end) so test-level GetProfilesByUserCube
	// can still observe the rows the goroutine inserted.
	t.Cleanup(func() {
		cctx := context.Background()
		cleanupWindowCharsCube(cctx, t, pg, cube)
		if pool := pg.Pool(); pool != nil {
			_, _ = pool.Exec(cctx,
				`DELETE FROM memos_graph.user_profiles WHERE user_id = $1`, cube)
		}
	})

	var counts [2]int64
	stub := stubProfileLLMServer(t, profileBody, &counts)
	defer stub.Close()

	h := &Handler{
		logger:       logger,
		postgres:     pg,
		embedder:     &stubEmbedder{},
		llmExtractor: llm.NewLLMExtractor(stub.URL, "test-key", "stub-model", nil, logger),
	}

	cubeStr := cube
	_, err := h.nativeFineAddForCube(ctx, &fullAddRequest{
		UserID:          &cubeStr,
		Mode:            strPtr(modeFine),
		WritableCubeIDs: []string{cube},
		Messages:        messages,
	}, cube)
	if err != nil {
		t.Fatalf("nativeFineAddForCube failed: %v", err)
	}

	// Wait up to 10s for the fire-and-forget goroutine to BulkUpsert.
	// Poll the user_profiles table directly so we don't race the post-LLM commit.
	pollDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(pollDeadline) {
		rows, err := pg.GetProfilesByUserCube(ctx, cube, cube)
		if err == nil && len(rows) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	return cube, cube, atomic.LoadInt64(&counts[1]), pg
}

func TestLivePG_FineAdd_ProfileExtract_BiographyPositive(t *testing.T) {
	// Memobase-format profile body covering all three required tuples.
	profileBody := `[POSSIBLE TOPICS THINKING about Alice]
---
- basic_info	name	alice
- work	company	acme
- work	title	software engineer
- interest	movie	Inception, Interstellar
`
	_, userID, profileCalls, pg := runProfileExtractScenario(t,
		"livepg-fine-profile-pos",
		profileBody,
		[]chatMessage{
			{Role: "user", Content: "Hi! My name is Alice and I work at Acme as a software engineer.", ChatTime: "2026-04-27T10:00:00"},
			{Role: "assistant", Content: "Nice to meet you, Alice. What kind of work do you do?", ChatTime: "2026-04-27T10:00:05"},
			{Role: "user", Content: "Backend systems, mostly. I love movies in my free time — Inception and Interstellar are my favourites.", ChatTime: "2026-04-27T10:00:30"},
			{Role: "assistant", Content: "Great picks!", ChatTime: "2026-04-27T10:00:35"},
		},
	)
	if profileCalls < 1 {
		t.Fatalf("expected ≥1 profile-extract LLM call, got %d", profileCalls)
	}

	ctx := context.Background()
	rows, err := pg.GetProfilesByUserCube(ctx, userID, userID)
	if err != nil {
		t.Fatalf("GetProfilesByUserCube: %v", err)
	}
	if len(rows) < 3 {
		t.Fatalf("want ≥3 profile rows, got %d (rows=%+v)", len(rows), rows)
	}

	want := map[string]bool{
		"basic_info|name": true,
		"work|company":    true,
		"interest|movie":  true,
	}
	for _, r := range rows {
		key := r.Topic + "|" + r.SubTopic
		delete(want, key)
	}
	if len(want) > 0 {
		var missing []string
		for k := range want {
			missing = append(missing, k)
		}
		t.Errorf("missing expected (topic|sub_topic) tuples: %v\nrows: %+v", missing, rows)
	}
}

func TestLivePG_FineAdd_ProfileExtract_PureCodeNegative(t *testing.T) {
	// Empty profile body — extractor returns 0 entries → no rows persisted.
	profileBody := `[POSSIBLE TOPICS THINKING — no user info found]
---
`
	_, userID, _, pg := runProfileExtractScenario(t,
		"livepg-fine-profile-neg",
		profileBody,
		[]chatMessage{
			{Role: "user", Content: "func add(a int, b int) int { return a + b }", ChatTime: "2026-04-27T11:00:00"},
			{Role: "assistant", Content: "That's a simple addition function in Go.", ChatTime: "2026-04-27T11:00:05"},
			{Role: "user", Content: "What about subtraction?", ChatTime: "2026-04-27T11:00:10"},
			{Role: "assistant", Content: "func sub(a int, b int) int { return a - b }", ChatTime: "2026-04-27T11:00:15"},
		},
	)

	ctx := context.Background()
	rows, err := pg.GetProfilesByUserCube(ctx, userID, userID)
	if err != nil {
		t.Fatalf("GetProfilesByUserCube: %v", err)
	}
	if len(rows) > 1 {
		t.Errorf("pure-code conversation should yield ≤1 profile row, got %d (rows=%+v)", len(rows), rows)
	}
}
