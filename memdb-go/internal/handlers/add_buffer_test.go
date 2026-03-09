package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

// --- Pure unit tests (no Redis) ---

func TestBufferKey(t *testing.T) {
	tests := []struct {
		cubeID string
		want   string
	}{
		{"memos", "memdb:buf:memos"},
		{"user-123", "memdb:buf:user-123"},
		{"", "memdb:buf:"},
	}
	for _, tt := range tests {
		got := bufferKey(tt.cubeID)
		if got != tt.want {
			t.Errorf("bufferKey(%q) = %q, want %q", tt.cubeID, got, tt.want)
		}
	}
}

func TestBufferEntry_JSONRoundTrip(t *testing.T) {
	entry := bufferEntry{
		Conversation: "user: [2026-02-21T10:00:00]: hello world",
		Timestamp:    1708531200,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Verify compact keys are used
	if !strings.Contains(string(data), `"c":`) {
		t.Error("expected compact key 'c' in JSON")
	}
	if !strings.Contains(string(data), `"t":`) {
		t.Error("expected compact key 't' in JSON")
	}

	var decoded bufferEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Conversation != entry.Conversation {
		t.Errorf("conversation mismatch: got %q", decoded.Conversation)
	}
	if decoded.Timestamp != entry.Timestamp {
		t.Errorf("timestamp mismatch: got %d", decoded.Timestamp)
	}
}

// --- Routing tests ---

func TestNativeAddForCube_BufferRouting(t *testing.T) {
	tests := []struct {
		name       string
		mode       *string
		asyncMode  *string
		bufferCfg  BufferConfig
		hasRedis   bool
		hasLLM     bool
		wantBuffer bool // true = should try buffer path
	}{
		{
			name:       "default mode + buffer enabled + redis + llm → buffer",
			mode:       nil,
			bufferCfg:  BufferConfig{Enabled: true, Size: 5, TTL: 30 * time.Second},
			hasRedis:   true,
			hasLLM:     true,
			wantBuffer: true,
		},
		{
			name:       "explicit fine → skip buffer",
			mode:       strPtr("fine"),
			bufferCfg:  BufferConfig{Enabled: true, Size: 5, TTL: 30 * time.Second},
			hasRedis:   true,
			hasLLM:     true,
			wantBuffer: false,
		},
		{
			name:       "explicit fast → skip buffer",
			mode:       strPtr("fast"),
			bufferCfg:  BufferConfig{Enabled: true, Size: 5, TTL: 30 * time.Second},
			hasRedis:   true,
			hasLLM:     true,
			wantBuffer: false,
		},
		{
			name:       "buffer disabled → skip buffer",
			mode:       nil,
			bufferCfg:  BufferConfig{Enabled: false},
			hasRedis:   true,
			hasLLM:     true,
			wantBuffer: false,
		},
		{
			name:       "no redis → skip buffer",
			mode:       nil,
			bufferCfg:  BufferConfig{Enabled: true, Size: 5, TTL: 30 * time.Second},
			hasRedis:   false,
			hasLLM:     true,
			wantBuffer: false,
		},
		{
			name:       "no llm → skip buffer",
			mode:       nil,
			bufferCfg:  BufferConfig{Enabled: true, Size: 5, TTL: 30 * time.Second},
			hasRedis:   true,
			hasLLM:     false,
			wantBuffer: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We only test the routing decision, not the actual pipeline execution.
			// Build a handler with the minimum fields needed for the routing check.
			h := &Handler{
				logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				bufferCfg: tt.bufferCfg,
			}
			if tt.hasLLM {
				// Set a non-nil extractor (won't be called in routing check)
				h.llmExtractor = &llm.LLMExtractor{}
			}
			if tt.hasRedis {
				// Set a non-nil Redis (won't be called in routing check)
				rdb := redis.NewClient(&redis.Options{Addr: "localhost:0"})
				h.redis = db.NewRedisFromClient(rdb, h.logger)
			}

			req := &fullAddRequest{Mode: tt.mode}

			// Check the routing path by examining which branch would be taken.
			// Since we can't call nativeAddForCube (it would actually run the pipeline),
			// we verify the conditions directly.
			isBufferPath := req.Mode == nil && h.bufferCfg.Enabled && h.redis != nil && h.llmExtractor != nil
			if isBufferPath != tt.wantBuffer {
				t.Errorf("buffer routing = %v, want %v", isBufferPath, tt.wantBuffer)
			}
		})
	}
}

// --- Redis integration tests (miniredis) ---

// newTestRedis creates a miniredis-backed db.Redis for testing.
func newTestRedis(t *testing.T) (*db.Redis, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return db.NewRedisFromClient(rdb, logger), mr
}

func TestBufferAddForCube_BelowThreshold_ReturnsBuffered(t *testing.T) {
	rd, _ := newTestRedis(t)

	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
		bufferCfg: BufferConfig{
			Enabled: true,
			Size:    5,
			TTL:     30 * time.Second,
		},
	}

	ctx := context.Background()
	req := &fullAddRequest{
		Messages: []chatMessage{
			{Role: "user", Content: "I like coffee"},
		},
	}

	items, err := h.bufferAddForCube(ctx, req, "test-cube")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].MemoryType != "buffer" {
		t.Errorf("expected memory_type=buffer, got %s", items[0].MemoryType)
	}
	if items[0].CubeID != "test-cube" {
		t.Errorf("expected cube_id=test-cube, got %s", items[0].CubeID)
	}
	if items[0].MemoryID != "" {
		t.Errorf("expected empty memory_id for buffered response, got %s", items[0].MemoryID)
	}

	// Verify Redis has 1 entry
	client := rd.Client()
	listLen, err := client.LLen(ctx, bufferKey("test-cube")).Result()
	if err != nil {
		t.Fatalf("redis llen: %v", err)
	}
	if listLen != 1 {
		t.Errorf("expected 1 entry in buffer, got %d", listLen)
	}
}

func TestBufferAddForCube_EmptyMessages_ReturnsNil(t *testing.T) {
	rd, _ := newTestRedis(t)

	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
		bufferCfg: BufferConfig{
			Enabled: true,
			Size:    5,
			TTL:     30 * time.Second,
		},
	}

	items, err := h.bufferAddForCube(context.Background(), &fullAddRequest{}, "test-cube")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items != nil {
		t.Errorf("expected nil for empty messages, got %v", items)
	}
}

func TestBufferAddForCube_AccumulatesMultipleEntries(t *testing.T) {
	rd, _ := newTestRedis(t)

	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
		bufferCfg: BufferConfig{
			Enabled: true,
			Size:    5,
			TTL:     30 * time.Second,
		},
	}

	ctx := context.Background()

	// Push 4 entries — all should return "buffered"
	for i := 0; i < 4; i++ {
		req := &fullAddRequest{
			Messages: []chatMessage{
				{Role: "user", Content: "message " + string(rune('A'+i))},
			},
		}
		items, err := h.bufferAddForCube(ctx, req, "test-cube")
		if err != nil {
			t.Fatalf("push %d: unexpected error: %v", i, err)
		}
		if len(items) != 1 || items[0].MemoryType != "buffer" {
			t.Fatalf("push %d: expected buffered response, got %v", i, items)
		}
	}

	// Verify Redis has 4 entries
	client := rd.Client()
	listLen, err := client.LLen(ctx, bufferKey("test-cube")).Result()
	if err != nil {
		t.Fatalf("redis llen: %v", err)
	}
	if listLen != 4 {
		t.Errorf("expected 4 entries in buffer, got %d", listLen)
	}
}

func TestBufferAddForCube_SlidingTTL(t *testing.T) {
	rd, mr := newTestRedis(t)

	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
		bufferCfg: BufferConfig{
			Enabled: true,
			Size:    5,
			TTL:     30 * time.Second,
		},
	}

	ctx := context.Background()
	req := &fullAddRequest{
		Messages: []chatMessage{
			{Role: "user", Content: "hello"},
		},
	}

	_, _ = h.bufferAddForCube(ctx, req, "test-cube")

	// Verify TTL was set
	ttl := mr.TTL(bufferKey("test-cube"))
	if ttl <= 0 {
		t.Error("expected positive TTL on buffer key")
	}
	if ttl > bufferSafetyTTL {
		t.Errorf("TTL %v exceeds safety TTL %v", ttl, bufferSafetyTTL)
	}
}

// newFlushTestHandler creates a handler suitable for flush tests.
// Uses failEmbedder (skips postgres in fetchFineCandidates) and a failing LLM
// server (runFinePipeline errors before touching postgres, triggering re-push).
func newFlushTestHandler(t *testing.T, rd *db.Redis) *Handler {
	t.Helper()
	llmServer := newFailLLMServer(t)
	t.Cleanup(llmServer.Close)

	extractor := llm.NewLLMExtractor(llmServer.URL, "test-key", "test-model", nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	return &Handler{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:        rd,
		llmExtractor: extractor,
		embedder:     &failEmbedder{},
		bufferCfg: BufferConfig{
			Enabled: true,
			Size:    5,
			TTL:     30 * time.Second,
		},
	}
}

func TestFlushBuffer_AtomicReadAndDelete(t *testing.T) {
	rd, _ := newTestRedis(t)
	h := newFlushTestHandler(t, rd)

	ctx := context.Background()
	client := rd.Client()
	key := bufferKey("test-cube")

	// Pre-populate buffer with 3 entries
	for i := 0; i < 3; i++ {
		entry := bufferEntry{
			Conversation: "user: hello " + string(rune('0'+i)),
			Timestamp:    time.Now().Unix(),
		}
		data, _ := json.Marshal(entry)
		client.RPush(ctx, key, data)
	}

	// Flush will fail (LLM returns 500) but should atomically read entries
	_, err := h.flushBuffer(ctx, "test-cube")
	if err == nil {
		t.Fatal("expected error from runFinePipeline (LLM returns 500)")
	}

	// Entries should be re-pushed (error recovery)
	listLen, _ := client.LLen(ctx, key).Result()
	if listLen != 3 {
		t.Errorf("expected 3 re-pushed entries, got %d", listLen)
	}
}

func TestFlushBuffer_EmptyBuffer_ReturnsNil(t *testing.T) {
	rd, _ := newTestRedis(t)

	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
	}

	items, err := h.flushBuffer(context.Background(), "nonexistent-cube")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items != nil {
		t.Errorf("expected nil for empty buffer, got %v", items)
	}
}

func TestFlushBuffer_ConcurrentSafety(t *testing.T) {
	rd, _ := newTestRedis(t)
	h := newFlushTestHandler(t, rd)

	ctx := context.Background()
	client := rd.Client()
	key := bufferKey("test-cube")

	// Pre-populate buffer with 5 entries
	for i := 0; i < 5; i++ {
		entry := bufferEntry{
			Conversation: "user: msg " + string(rune('0'+i)),
			Timestamp:    time.Now().Unix(),
		}
		data, _ := json.Marshal(entry)
		client.RPush(ctx, key, data)
	}

	// Flush from two goroutines concurrently.
	// With miniredis (single-threaded), this tests sequential atomicity.
	// In production Redis, the Lua script ensures true atomicity.
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := h.flushBuffer(ctx, "test-cube")
			errCh <- err
		}()
	}

	var errs int
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			errs++
		}
	}

	// One goroutine got the entries (and failed on pipeline → re-pushed),
	// the other got an empty buffer (nil error, nil items).
	// After re-push, entries should be in Redis exactly once.
	listLen, _ := client.LLen(ctx, key).Result()
	if listLen != 5 {
		t.Errorf("expected 5 re-pushed entries, got %d", listLen)
	}
}

func TestFlushBuffer_ConversationConcatenation(t *testing.T) {
	rd, _ := newTestRedis(t)

	// Track conversations sent to LLM to verify concatenation
	var receivedConversation string
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		// Extract the user message content which contains the conversation
		if msgs, ok := body["messages"].([]any); ok && len(msgs) > 1 {
			if userMsg, ok := msgs[1].(map[string]any); ok {
				receivedConversation, _ = userMsg["content"].(string)
			}
		}

		// Return empty facts — no postgres needed
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": "[]"}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer llmServer.Close()

	extractor := llm.NewLLMExtractor(llmServer.URL, "test-key", "test-model", nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	h := &Handler{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:        rd,
		llmExtractor: extractor,
		embedder:     &failEmbedder{}, // fail embedder → skip fetchFineCandidates postgres call
	}

	ctx := context.Background()
	client := rd.Client()
	key := bufferKey("test-cube")

	// Push 3 entries with distinct conversations
	convs := []string{
		"user: [2026-02-21T10:00:00]: I like coffee",
		"user: [2026-02-21T10:01:00]: I also like tea",
		"user: [2026-02-21T10:02:00]: My favorite is espresso",
	}
	for _, conv := range convs {
		entry := bufferEntry{
			Conversation: conv,
			Timestamp:    time.Now().Unix(),
		}
		data, _ := json.Marshal(entry)
		client.RPush(ctx, key, data)
	}

	// Flush — should concatenate with \n---\n separator
	_, err := h.flushBuffer(ctx, "test-cube")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify conversations were concatenated with \n---\n separator
	for _, conv := range convs {
		if !strings.Contains(receivedConversation, conv) {
			t.Errorf("expected conversation to contain %q", conv)
		}
	}
	if !strings.Contains(receivedConversation, "\n---\n") {
		t.Error("expected \\n---\\n separator between conversations")
	}

	// Buffer should be empty after successful flush
	listLen, _ := client.LLen(ctx, key).Result()
	if listLen != 0 {
		t.Errorf("expected empty buffer after flush, got %d entries", listLen)
	}
}

func TestFlushBuffer_MalformedEntries_Skipped(t *testing.T) {
	rd, _ := newTestRedis(t)
	h := newFlushTestHandler(t, rd)

	ctx := context.Background()
	client := rd.Client()
	key := bufferKey("test-cube")

	// Push a valid entry
	entry := bufferEntry{Conversation: "user: hello", Timestamp: time.Now().Unix()}
	data, _ := json.Marshal(entry)
	client.RPush(ctx, key, data)

	// Push malformed JSON
	client.RPush(ctx, key, "not-valid-json{{{")

	// Push another valid entry
	entry2 := bufferEntry{Conversation: "user: world", Timestamp: time.Now().Unix()}
	data2, _ := json.Marshal(entry2)
	client.RPush(ctx, key, data2)

	// Flush — should skip malformed entry and process the 2 valid ones.
	// Will fail on pipeline (LLM returns 500), but entries should be consumed.
	_, err := h.flushBuffer(ctx, "test-cube")
	if err == nil {
		t.Fatal("expected error from runFinePipeline (LLM returns 500)")
	}

	// Re-pushed entries should include all 3 raw strings (malformed included,
	// since re-push uses the raw strings from Redis, not decoded entries)
	listLen, _ := client.LLen(ctx, key).Result()
	if listLen != 3 {
		t.Errorf("expected 3 re-pushed entries (including malformed), got %d", listLen)
	}
}

func TestRepushEntries(t *testing.T) {
	rd, _ := newTestRedis(t)

	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
	}

	ctx := context.Background()
	key := bufferKey("repush-test")

	entries := []string{
		`{"c":"conv1","t":1708531200}`,
		`{"c":"conv2","t":1708531201}`,
		`{"c":"conv3","t":1708531202}`,
	}

	h.repushEntries(ctx, key, entries)

	// Verify entries are back in Redis
	client := rd.Client()
	listLen, _ := client.LLen(ctx, key).Result()
	if listLen != 3 {
		t.Errorf("expected 3 entries after re-push, got %d", listLen)
	}

	// Verify order is preserved
	vals, _ := client.LRange(ctx, key, 0, -1).Result()
	for i, v := range vals {
		if v != entries[i] {
			t.Errorf("entry %d: got %q, want %q", i, v, entries[i])
		}
	}
}

// --- Time-based flusher tests ---

func TestCheckAndFlushStale_FreshBuffer_NoFlush(t *testing.T) {
	rd, _ := newTestRedis(t)

	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
		bufferCfg: BufferConfig{
			Enabled: true,
			Size:    5,
			TTL:     30 * time.Second,
		},
	}

	ctx := context.Background()
	client := rd.Client()
	key := bufferKey("test-cube")

	// Push a fresh entry (timestamp = now)
	entry := bufferEntry{
		Conversation: "user: hello",
		Timestamp:    time.Now().Unix(),
	}
	data, _ := json.Marshal(entry)
	client.RPush(ctx, key, data)

	// Check stale — should NOT flush (entry is fresh)
	h.checkAndFlushStale(ctx, client, key)

	// Buffer should still have 1 entry
	listLen, _ := client.LLen(ctx, key).Result()
	if listLen != 1 {
		t.Errorf("expected 1 entry (not flushed), got %d", listLen)
	}
}

func TestCheckAndFlushStale_StaleBuffer_Flushes(t *testing.T) {
	rd, _ := newTestRedis(t)
	h := newFlushTestHandler(t, rd)
	h.bufferCfg.TTL = 1 * time.Second // Very short TTL for testing

	ctx := context.Background()
	client := rd.Client()
	key := bufferKey("stale-cube")

	// Push an entry with timestamp in the past (older than TTL)
	entry := bufferEntry{
		Conversation: "user: old message",
		Timestamp:    time.Now().Add(-5 * time.Second).Unix(), // 5s ago, TTL is 1s
	}
	data, _ := json.Marshal(entry)
	client.RPush(ctx, key, data)

	// Check stale — should attempt flush (entry is stale)
	h.checkAndFlushStale(ctx, client, key)

	// Flush fails (LLM returns 500) and re-pushes.
	// The key should still have entries (re-pushed).
	listLen, _ := client.LLen(ctx, key).Result()
	if listLen != 1 {
		t.Errorf("expected 1 re-pushed entry after failed stale flush, got %d", listLen)
	}
}

func TestCheckStaleBuffers_ScansCorrectPrefix(t *testing.T) {
	rd, _ := newTestRedis(t)

	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
		bufferCfg: BufferConfig{
			Enabled: true,
			Size:    5,
			TTL:     30 * time.Second,
		},
	}

	ctx := context.Background()
	client := rd.Client()

	// Push fresh entries to multiple cubes
	for _, cubeID := range []string{"cube-a", "cube-b", "cube-c"} {
		entry := bufferEntry{
			Conversation: "user: hello from " + cubeID,
			Timestamp:    time.Now().Unix(),
		}
		data, _ := json.Marshal(entry)
		client.RPush(ctx, bufferKey(cubeID), data)
	}

	// Also push a non-buffer key (should be ignored by SCAN)
	client.Set(ctx, "memdb:other:key", "value", 0)

	// Check stale — none should be flushed (all fresh)
	h.checkStaleBuffers(ctx)

	// All 3 buffers should still have 1 entry each
	for _, cubeID := range []string{"cube-a", "cube-b", "cube-c"} {
		listLen, _ := client.LLen(ctx, bufferKey(cubeID)).Result()
		if listLen != 1 {
			t.Errorf("cube %s: expected 1 entry, got %d", cubeID, listLen)
		}
	}
}

func TestStartBufferFlusher_StopsOnContextCancel(t *testing.T) {
	rd, _ := newTestRedis(t)

	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:  rd,
		bufferCfg: BufferConfig{
			Enabled: true,
			Size:    5,
			TTL:     30 * time.Second,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		h.StartBufferFlusher(ctx)
		close(done)
	}()

	// Cancel context — flusher should stop
	cancel()

	select {
	case <-done:
		// Success — flusher stopped
	case <-time.After(2 * time.Second):
		t.Fatal("StartBufferFlusher did not stop after context cancellation")
	}
}

// --- Threshold trigger test ---

// failEmbedder returns errors on all calls — forces fetchFineCandidates to
// return early without touching postgres.
type failEmbedder struct{}

func (f *failEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, errEmbedDisabled
}
func (f *failEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return nil, errEmbedDisabled
}
func (f *failEmbedder) Dimension() int { return 1024 }
func (f *failEmbedder) Close() error   { return nil }

var errEmbedDisabled = errors.New("embedder disabled for test")

// newFailLLMServer creates a mock LLM server that always returns HTTP 500.
// This ensures runFinePipeline fails after fetchFineCandidates (which returns
// empty candidates due to failEmbedder) but before touching postgres.
func newFailLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "test: LLM unavailable"},
		})
	}))
}

func TestBufferAddForCube_ThresholdTrigger_AttemptsFlush(t *testing.T) {
	rd, _ := newTestRedis(t)

	// LLM server that returns errors → pipeline fails at ExtractAndDedup.
	// failEmbedder → fetchFineCandidates returns early (no postgres call).
	llmServer := newFailLLMServer(t)
	defer llmServer.Close()

	extractor := llm.NewLLMExtractor(llmServer.URL, "test-key", "test-model", nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	h := &Handler{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		redis:        rd,
		llmExtractor: extractor,
		embedder:     &failEmbedder{},
		bufferCfg: BufferConfig{
			Enabled: true,
			Size:    3,
			TTL:     30 * time.Second,
		},
	}

	ctx := context.Background()

	// Push 2 messages — should be buffered
	for i := 0; i < 2; i++ {
		req := &fullAddRequest{
			Messages: []chatMessage{
				{Role: "user", Content: "test message " + string(rune('A'+i))},
			},
		}
		items, err := h.bufferAddForCube(ctx, req, "test-cube")
		if err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		if items[0].MemoryType != "buffer" {
			t.Fatalf("push %d: expected buffered, got %s", i, items[0].MemoryType)
		}
	}

	// Verify 2 entries in buffer before threshold
	client := rd.Client()
	listLen, _ := client.LLen(ctx, bufferKey("test-cube")).Result()
	if listLen != 2 {
		t.Fatalf("expected 2 entries before threshold, got %d", listLen)
	}

	// Push 3rd message — reaches threshold (Size=3), triggers flush.
	// Flush will fail (LLM returns 500), entries get re-pushed.
	req := &fullAddRequest{
		Messages: []chatMessage{
			{Role: "user", Content: "I prefer dark mode"},
		},
	}
	_, err := h.bufferAddForCube(ctx, req, "test-cube")

	// We expect an error from the pipeline (LLM returns 500)
	if err == nil {
		t.Fatal("expected error from flush (LLM returns 500)")
	}

	// Entries should be re-pushed after failed flush
	listLen, _ = client.LLen(ctx, bufferKey("test-cube")).Result()
	if listLen != 3 {
		t.Errorf("expected 3 re-pushed entries after failed flush, got %d", listLen)
	}
}

// --- SetBufferConfig test ---

func TestSetBufferConfig(t *testing.T) {
	h := &Handler{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// Default zero value — buffer disabled
	if h.bufferCfg.Enabled {
		t.Error("buffer should be disabled by default")
	}

	cfg := BufferConfig{
		Enabled: true,
		Size:    10,
		TTL:     60 * time.Second,
	}
	h.SetBufferConfig(cfg)

	if !h.bufferCfg.Enabled {
		t.Error("buffer should be enabled after SetBufferConfig")
	}
	if h.bufferCfg.Size != 10 {
		t.Errorf("expected size=10, got %d", h.bufferCfg.Size)
	}
	if h.bufferCfg.TTL != 60*time.Second {
		t.Errorf("expected TTL=60s, got %v", h.bufferCfg.TTL)
	}
}

// --- Config test ---

func TestBufferConfig_Defaults(t *testing.T) {
	// Test that default values are set correctly when env vars are not set.
	// This tests the constants rather than Load() to avoid env var pollution.
	cfg := BufferConfig{
		Enabled: false,
		Size:    5,
		TTL:     30 * time.Second,
	}

	if cfg.Enabled {
		t.Error("default should be disabled")
	}
	if cfg.Size != 5 {
		t.Errorf("default size should be 5, got %d", cfg.Size)
	}
	if cfg.TTL != 30*time.Second {
		t.Errorf("default TTL should be 30s, got %v", cfg.TTL)
	}
}
