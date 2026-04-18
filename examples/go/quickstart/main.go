// MemDB Go quickstart — add memories and search them via the REST API.
//
// Usage:
//
//	go run main.go
//	MEMDB_API_KEY=your-key go run main.go
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
	defaultBase = "http://localhost:8080"
	userID      = "quickstart-user"
	cubeID      = "quickstart-cube"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type addRequest struct {
	UserID          string    `json:"user_id"`
	WritableCubeIDs []string  `json:"writable_cube_ids"`
	Messages        []message `json:"messages"`
	AsyncMode       string    `json:"async_mode"`
}

type searchRequest struct {
	UserID          string   `json:"user_id"`
	ReadableCubeIDs []string `json:"readable_cube_ids"`
	Query           string   `json:"query"`
	TopK            int      `json:"top_k"`
	Mode            string   `json:"mode"`
}

type memoryItem struct {
	Memory  string  `json:"memory"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

type searchData struct {
	TextMem []memoryItem `json:"text_mem"`
}

type searchResponse struct {
	Data searchData `json:"data"`
}

func post(client *http.Client, base, path string, apiKey string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, data)
	}
	return data, nil
}

func main() {
	base := os.Getenv("MEMDB_URL")
	apiKey := os.Getenv("MEMDB_API_KEY")
	if base == "" {
		base = defaultBase
	}

	client := &http.Client{Timeout: 30 * time.Second}
	fmt.Printf("MemDB quickstart — %s\n\n", base)

	conversations := [][]message{
		{
			{Role: "user", Content: "I love hiking in the mountains on weekends."},
			{Role: "assistant", Content: "Great! I'll remember that you enjoy mountain hiking."},
		},
		{
			{Role: "user", Content: "My favourite programming language is Go."},
			{Role: "assistant", Content: "Noted — Go is your preferred language."},
		},
		{
			{Role: "user", Content: "I'm allergic to peanuts."},
			{Role: "assistant", Content: "Important — I'll keep your peanut allergy in mind."},
		},
	}

	fmt.Println("1. Adding 3 memories...")
	for _, msgs := range conversations {
		_, err := post(client, base, "/product/add", apiKey, addRequest{
			UserID:          userID,
			WritableCubeIDs: []string{cubeID},
			Messages:        msgs,
			AsyncMode:       "sync",
		})
		if err != nil {
			log.Fatalf("add failed: %v", err)
		}
		fmt.Println("  added (200)")
	}

	query := "outdoor activities"
	fmt.Printf("\n2. Searching for %q...\n", query)
	raw, err := post(client, base, "/product/search", apiKey, searchRequest{
		UserID:          userID,
		ReadableCubeIDs: []string{cubeID},
		Query:           query,
		TopK:            3,
		Mode:            "fast",
	})
	if err != nil {
		log.Fatalf("search failed: %v", err)
	}

	var sr searchResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		log.Fatalf("parse failed: %v", err)
	}

	fmt.Printf("\n3. Top-%d results:\n", len(sr.Data.TextMem))
	for i, m := range sr.Data.TextMem {
		text := m.Memory
		if text == "" {
			text = m.Content
		}
		fmt.Printf("  [%d] score=%.3f  %s\n", i+1, m.Score, text)
	}
	fmt.Println("\nDone.")
}
