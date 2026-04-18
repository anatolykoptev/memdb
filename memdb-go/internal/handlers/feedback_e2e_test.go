//go:build integration

package handlers

// feedback_e2e_test.go — E2E integration test for POST /product/feedback.
// Requires real Postgres + HTTP embedder + LLM proxy.
// Run with:
//   MEMDB_TEST_DB_URL=postgres://... MEMDB_EMBED_URL=http://... LLM_API_BASE=http://.../v1 LLM_API_KEY=... \
//   go test -tags=integration ./internal/handlers/ -run TestValidatedFeedback_E2E -v -count=1

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

func TestValidatedFeedback_E2E(t *testing.T) {
	// --- resolve env vars ---
	dbURL := os.Getenv("MEMDB_TEST_DB_URL")
	if dbURL == "" {
		dbURL = os.Getenv("MEMDB_TEST_POSTGRES_URL") // fallback to existing db-package convention
	}
	embedURL := os.Getenv("MEMDB_EMBED_URL")
	llmBase := os.Getenv("LLM_API_BASE") // e.g. http://127.0.0.1:8317/v1
	llmKey := os.Getenv("LLM_API_KEY")

	if dbURL == "" || embedURL == "" || llmBase == "" {
		t.Skip("MEMDB_TEST_DB_URL (or MEMDB_TEST_POSTGRES_URL), MEMDB_EMBED_URL, and LLM_API_BASE must be set")
	}

	// llm.NewClient appends /v1/chat/completions — strip trailing /v1 if present.
	llmBaseClean := strings.TrimRight(strings.TrimSuffix(strings.TrimRight(llmBase, "/"), "/v1"), "/")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	logger := slog.Default()

	// --- wire real dependencies ---
	pg, err := db.NewPostgres(ctx, dbURL, logger)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	// pg.Close registered first → runs last (t.Cleanup is LIFO).
	t.Cleanup(func() { pg.Close() })

	// HTTPEmbedder targets production embed-server (not ONNX — no local model files needed).
	emb := embedder.NewHTTPEmbedder(embedURL, "multilingual-e5-large", 1024, logger)

	llmClient := llm.NewClient(llmBaseClean, llmKey, "gemini-2.5-flash-lite", nil, logger)
	llmExt := llm.NewLLMExtractor(llmBaseClean, llmKey, "gemini-2.5-flash-lite", nil, logger)

	h := &Handler{
		logger:       logger,
		postgres:     pg,
		embedder:     emb,
		llmChat:      llmClient,
		llmExtractor: llmExt, // needed if LLM judge returns all-irrelevant → processPureAdd
	}

	// --- seed baseline memory ---
	testUser := "e2e-feedback-user-" + uuid.New().String()[:8]
	memID := uuid.New().String()
	seedText := "User likes red colour"

	vecs, err := emb.Embed(ctx, []string{seedText})
	if err != nil || len(vecs) == 0 {
		t.Fatalf("embed seed: %v", err)
	}
	props := map[string]any{
		"id": memID, "memory": seedText, "memory_type": "LongTermMemory",
		"user_name": testUser, "user_id": testUser,
		"status": "activated", "created_at": nowTimestamp(), "updated_at": nowTimestamp(),
		"confidence": 0.9, "source": "e2e_test",
	}
	propsJSON, _ := json.Marshal(props)
	seedNode := db.MemoryInsertNode{
		ID: memID, PropertiesJSON: propsJSON, EmbeddingVec: db.FormatVector(vecs[0]),
	}
	if err := pg.InsertMemoryNodes(ctx, []db.MemoryInsertNode{seedNode}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	// cleanup seed data — registered second, runs before pg.Close (t.Cleanup is LIFO).
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()
		if _, err := pg.DeleteByPropertyIDs(cleanCtx, []string{memID}, testUser); err != nil {
			t.Logf("cleanup warning: %v", err)
		}
	})

	// --- build request ---
	history := []map[string]string{
		{"role": "user", "content": "I like red"},
		{"role": "assistant", "content": "Noted, you like red."},
	}
	historyJSON, _ := json.Marshal(history)
	payload := map[string]any{
		"user_id":          testUser,
		"feedback_content": "Actually I prefer blue, not red",
		"history":          json.RawMessage(historyJSON),
	}
	body, _ := json.Marshal(payload)

	r := httptest.NewRequest(http.MethodPost, "/product/feedback", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ValidatedFeedback(w, r)

	// --- assertions ---
	if w.Code != http.StatusOK {
		t.Fatalf("want HTTP 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v — body: %s", err, w.Body.String())
	}
	if resp["code"] != float64(200) {
		t.Fatalf("want code=200 in envelope, got %v — body: %s", resp["code"], w.Body.String())
	}

	dataVal, hasDat := resp["data"]
	if !hasDat || dataVal == nil {
		t.Logf("WARNING: data field is nil — LLM judge may have returned all-irrelevant (acceptable)")
	} else {
		t.Logf("data: %v", dataVal)
	}
}
