// mcp-stdio-proxy — lightweight STDIO-to-HTTP bridge for MCP servers.
//
// Reads newline-delimited JSON-RPC from stdin, POSTs to a Streamable HTTP
// MCP endpoint, and writes responses to stdout. Zero external dependencies,
// instant startup, no model loading.
//
// Usage: mcp-stdio-proxy [--url http://localhost:8001/mcp]
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	clientTimeout    = 120 * time.Second
	scannerBufInit   = 1024 * 1024  // 1MB initial buffer
	scannerBufMax    = 10 * 1024 * 1024 // 10MB max line
)

func main() {
	url := "http://127.0.0.1:8001/mcp"
	for i, arg := range os.Args[1:] {
		if arg == "--url" && i+1 < len(os.Args)-1 {
			url = os.Args[i+2]
		}
	}

	client := &http.Client{Timeout: clientTimeout}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, scannerBufInit), scannerBufMax) // 10MB max line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := forward(client, url, line); err != nil {
			writeError(line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "stdin read error: %v\n", err)
		os.Exit(1)
	}
}

func forward(client *http.Client, url string, body []byte) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, scannerBufInit), scannerBufMax)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				os.Stdout.WriteString(line[6:] + "\n")
			}
		}
		return scanner.Err()
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	os.Stdout.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		os.Stdout.WriteString("\n")
	}
	return nil
}

func writeError(reqLine []byte, err error) {
	var id any
	var parsed map[string]any
	if json.Unmarshal(reqLine, &parsed) == nil {
		id = parsed["id"]
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": -32603, "message": err.Error()},
	}
	data, _ := json.Marshal(resp)
	os.Stdout.Write(data)
	os.Stdout.WriteString("\n")
}
