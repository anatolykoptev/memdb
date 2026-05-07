package mcptools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleUpdateMemory_ProxiesToFullUpdateEndpoint verifies that
// handleUpdateMemory proxies the call to /product/update_memory on the
// memdb-go backend (full re-embed + CE cache clear path) rather than
// calling UpdateMemoryContent directly (text-only SQL, no re-embed).
//
// RED: fails until handleUpdateMemory is changed to use proxyCall.
func TestHandleUpdateMemory_ProxiesToFullUpdateEndpoint(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]any

	// Fake memdb-go server.
	fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"code":    200,
			"message": "memory updated",
			"data":    map[string]any{"memory_id": "mem-test-001"},
		})
	}))
	defer fakeBackend.Close()

	input := UpdateMemoryInput{
		MemoryID:      "mem-test-001",
		CubeID:        "user@example.com",
		MemoryContent: "updated text content",
	}

	_, result, err := handleUpdateMemory(context.Background(), nil, fakeBackend.URL, "test-secret", input)
	if err != nil {
		t.Fatalf("handleUpdateMemory returned error: %v", err)
	}
	_ = result

	// The proxy must hit /product/update_memory (full update path).
	if receivedPath != "/product/update_memory" {
		t.Errorf("expected proxy to /product/update_memory, got %q — text-only UpdateMemoryContent path still in use", receivedPath)
	}

	// Payload must contain memory_id, user_id (cube), and text fields.
	if receivedBody["memory_id"] != "mem-test-001" {
		t.Errorf("memory_id = %v, want mem-test-001", receivedBody["memory_id"])
	}
	if receivedBody["user_id"] != "user@example.com" {
		t.Errorf("user_id = %v, want user@example.com", receivedBody["user_id"])
	}
	if receivedBody["text"] != "updated text content" {
		t.Errorf("text = %v, want 'updated text content'", receivedBody["text"])
	}
}
