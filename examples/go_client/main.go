// Pure-Go MemDB client: add a memory, then search for it.
//
// Uses only net/http and encoding/json — no SDK needed. Demonstrates the
// minimal contract for MemDB integration from any Go service: build a JSON
// body, POST it, decode the {"code","message","data"} envelope.
//
// Run:
//
//	go run .
//
// Or with auth:
//
//	MEMDB_API_KEY=... go run .
//	MEMDB_SERVICE_SECRET=... go run .
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	defaultURL  = "http://localhost:8080"
	httpTimeout = 30 * time.Second
)

// Client wraps base URL and credentials for one MemDB instance.
type Client struct {
	BaseURL       string
	APIKey        string // optional Bearer token
	ServiceSecret string // optional X-Service-Secret
	HTTP          *http.Client
}

// post sends a JSON body to a MemDB endpoint and returns the raw response.
func (c *Client) post(path string, body any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.ServiceSecret != "" {
		req.Header.Set("X-Service-Secret", c.ServiceSecret)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, path, data)
	}
	return data, nil
}

// add stores one short conversation as memories. async_mode=sync blocks until
// the extraction pipeline has written, so a follow-up search sees the result.
func (c *Client) add(userID, cubeID string, messages []map[string]string) error {
	body := map[string]any{
		"user_id":            userID,
		"writable_cube_ids":  []string{cubeID},
		"messages":           messages,
		"async_mode":         "sync",
	}
	_, err := c.post("/product/add", body)
	return err
}

// SearchResult is the subset of /product/search response we care about.
type SearchResult struct {
	Data struct {
		// Newer servers return memories under "memories"; older ones under
		// "text_mem". We try both.
		Memories []memoryHit `json:"memories"`
		TextMem  []memoryHit `json:"text_mem"`
	} `json:"data"`
}

type memoryHit struct {
	Memory  string  `json:"memory"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// search runs a semantic query over the cube and returns up to topK hits.
func (c *Client) search(userID, cubeID, query string, topK int) ([]memoryHit, error) {
	body := map[string]any{
		"user_id":            userID,
		"readable_cube_ids":  []string{cubeID},
		"query":              query,
		"top_k":              topK,
		"mode":               "fast",
	}
	raw, err := c.post("/product/search", body)
	if err != nil {
		return nil, err
	}
	var sr SearchResult
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	hits := sr.Data.Memories
	if len(hits) == 0 {
		hits = sr.Data.TextMem
	}
	return hits, nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	c := &Client{
		BaseURL:       env("MEMDB_URL", defaultURL),
		APIKey:        os.Getenv("MEMDB_API_KEY"),
		ServiceSecret: os.Getenv("MEMDB_SERVICE_SECRET"),
		HTTP:          &http.Client{Timeout: httpTimeout},
	}
	userID := env("MEMDB_USER_ID", "go-client-demo")
	cubeID := env("MEMDB_CUBE_ID", "go-client-demo")

	fmt.Printf("MemDB Go client → %s\n\n", c.BaseURL)

	convo := []map[string]string{
		{"role": "user", "content": "I love hiking in the mountains on weekends."},
		{"role": "assistant", "content": "Got it — I'll remember your weekend hiking habit."},
	}
	if err := c.add(userID, cubeID, convo); err != nil {
		log.Fatalf("add: %v", err)
	}
	fmt.Println("[add]    stored conversation about hiking")

	query := "outdoor activities"
	fmt.Printf("[search] query: %q\n", query)
	hits, err := c.search(userID, cubeID, query, 3)
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		fmt.Println("  (no results yet — embed indexer may still be catching up; re-run)")
	}
	for i, h := range hits {
		text := h.Memory
		if text == "" {
			text = h.Content
		}
		fmt.Printf("  %d. score=%.2f  %s\n", i+1, h.Score, text)
	}
	fmt.Println("done.")
}
