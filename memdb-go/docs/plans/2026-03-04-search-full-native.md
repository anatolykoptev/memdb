# Full Native /product/search — Kill Python Proxy Fallback

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Eliminate the circular dependency (Go→Python→Go) by making `/product/search` 100% native Go — no proxy fallback.

**Architecture:** Go's SearchService already handles fast mode natively (Phase 3). We add: (1) SearXNG internet search as a parallel retrieval path, (2) LLM-based fine mode (enhance + recall), (3) remove all proxy fallback from the search handler. Python's `_fast_search` go_client calls become safe because Go never calls Python back.

**Tech Stack:** Go 1.26, errgroup, httpx→net/http (SearXNG), LLM via CLIProxyAPI (OpenAI-compatible), PolarDB, Qdrant, Redis cache.

**Key insight:** Go already has `enhance.go` (LLM pronoun/time resolution) and `llm_rerank.go` (LLM reranking). Fine mode reuses the same LLM infra — it's "fast search + LLM filter + optional recall search."

---

## Task 1: Add SearXNG Internet Search Client

**Files:**
- Create: `internal/search/internet.go`
- Create: `internal/search/internet_test.go`

**Context:** SearXNG runs at `http://searxng:8080` inside Docker. API: `GET /search?q=...&format=json&categories=general`. Returns JSON with `results[]` containing `title`, `content` (snippet), `url`. We embed these texts and merge into the search pipeline as an additional parallel path.

**Step 1: Write the test**

```go
package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternetSearch_ParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Error("expected format=json")
		}
		resp := searxngResponse{
			Results: []searxngResult{
				{Title: "Go testing", Content: "How to test in Go", URL: "https://example.com/1"},
				{Title: "Go concurrency", Content: "Goroutines and channels", URL: "https://example.com/2"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewInternetSearcher(srv.URL, 5)
	results, err := client.Search(context.Background(), "golang testing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Title != "Go testing" {
		t.Errorf("expected title 'Go testing', got %q", results[0].Title)
	}
}

func TestInternetSearch_EmptyOnError(t *testing.T) {
	client := NewInternetSearcher("http://localhost:1", 5)
	results, err := client.Search(context.Background(), "test")
	if err != nil {
		t.Fatalf("should not error, got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results on unreachable server, got %d", len(results))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd ./memdb-go && go test ./internal/search/ -run TestInternetSearch -v`
Expected: FAIL — types not defined

**Step 3: Implement internet.go**

```go
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const internetTimeout = 10 * time.Second

// InternetResult is a single web search result from SearXNG.
type InternetResult struct {
	Title   string
	Content string
	URL     string
}

// Text returns the combined text for embedding.
func (r InternetResult) Text() string {
	return r.Title + ": " + r.Content
}

// InternetSearcher queries SearXNG for web results.
type InternetSearcher struct {
	baseURL string
	limit   int
	client  *http.Client
}

// NewInternetSearcher creates a SearXNG client.
func NewInternetSearcher(baseURL string, limit int) *InternetSearcher {
	return &InternetSearcher{
		baseURL: strings.TrimRight(baseURL, "/"),
		limit:   limit,
		client:  &http.Client{Timeout: internetTimeout},
	}
}

type searxngResponse struct {
	Results []searxngResult `json:"results"`
}

type searxngResult struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	URL     string `json:"url"`
}

// Search queries SearXNG. Returns empty slice (not error) on failure — graceful degradation.
func (s *InternetSearcher) Search(ctx context.Context, query string) ([]InternetResult, error) {
	u := fmt.Sprintf("%s/search?q=%s&format=json&categories=general",
		s.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil // graceful: internet unavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	var body searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, nil
	}

	limit := s.limit
	if len(body.Results) < limit {
		limit = len(body.Results)
	}

	results := make([]InternetResult, 0, limit)
	for _, r := range body.Results[:limit] {
		if r.Title == "" && r.Content == "" {
			continue
		}
		results = append(results, InternetResult{
			Title:   r.Title,
			Content: r.Content,
			URL:     r.URL,
		})
	}
	return results, nil
}
```

**Step 4: Run test**

Run: `cd ./memdb-go && go test ./internal/search/ -run TestInternetSearch -v`
Expected: PASS

**Step 5: Commit**

```bash
cd ./memdb-go
git add internal/search/internet.go internal/search/internet_test.go
git commit -m "feat(search): add SearXNG internet search client

Graceful degradation — returns empty on failure, no errors propagated."
```

---

## Task 2: Wire Internet Search into SearchService Pipeline

**Files:**
- Modify: `internal/search/service.go` — add internet as parallel path
- Modify: `internal/search/config.go` — add `DefaultInternetLimit` constant
- Create: `internal/search/internet_pipeline_test.go`

**Context:** Internet results need to be: fetched → embedded → converted to `MergedResult` → merged into `textMerged` alongside vector/fulltext results. This happens as a new goroutine in `runParallelSearches`.

**Step 1: Add config constant**

In `internal/search/config.go`, add:
```go
const DefaultInternetLimit = 5 // max web results to embed and merge
```

**Step 2: Add InternetSearcher field to SearchService**

In `internal/search/service.go`, add to `SearchService` struct:
```go
Internet *InternetSearcher // nil = internet search disabled
```

Add to `parallelSearchResults`:
```go
internetResults []InternetResult
```

Add `InternetSearch` field to `SearchParams`:
```go
InternetSearch bool // enable web search via SearXNG
```

**Step 3: Add internet goroutine to runParallelSearches**

After `spawnWorkingMemAndGraph`, add call to new method:
```go
s.spawnInternetSearch(g, gctx, psr, p)
```

Implement:
```go
func (s *SearchService) spawnInternetSearch(
	g *errgroup.Group, ctx context.Context, psr *parallelSearchResults, p SearchParams,
) {
	if s.Internet == nil || !p.InternetSearch {
		return
	}
	g.Go(func() error {
		var err error
		psr.internetResults, err = s.Internet.Search(ctx, p.Query)
		if err != nil {
			s.logger.Debug("internet search failed", slog.Any("error", err))
		}
		return nil // never fail the pipeline
	})
}
```

**Step 4: Embed and merge internet results after parallel phase**

In `Search()` method, after `runParallelSearches` and before `mergeSearchResults`, add:
```go
internetMerged := s.embedInternetResults(ctx, psr.internetResults)
```

Then pass `internetMerged` to `mergeSearchResults` (add parameter). Inside merge, append to `textMerged`:
```go
textMerged = append(textMerged, internetMerged...)
```

Implement the embed helper:
```go
func (s *SearchService) embedInternetResults(ctx context.Context, results []InternetResult) []MergedResult {
	if len(results) == 0 || s.embedder == nil {
		return nil
	}
	texts := make([]string, len(results))
	for i, r := range results {
		texts[i] = r.Text()
	}
	vecs, err := s.embedder.Embed(ctx, texts)
	if err != nil {
		s.logger.Debug("internet embed failed", slog.Any("error", err))
		return nil
	}
	merged := make([]MergedResult, 0, len(results))
	for i, r := range results {
		if i >= len(vecs) {
			break
		}
		merged = append(merged, MergedResult{
			ID: "internet:" + r.URL,
			Properties: fmt.Sprintf(
				`{"memory": %q, "memory_type": "InternetMemory", "sources": [{"url": %q, "title": %q}]}`,
				r.Text(), r.URL, r.Title,
			),
			Score:     InternetBaseScore,
			Embedding: vecs[i],
		})
	}
	return merged
}
```

Add constant in `config.go`:
```go
const InternetBaseScore = 0.5 // base score for internet results before reranking
```

**Step 5: Write integration test**

```go
// internal/search/internet_pipeline_test.go
package search

import (
	"testing"
)

func TestEmbedInternetResults_Empty(t *testing.T) {
	svc := &SearchService{}
	got := svc.embedInternetResults(nil, nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}
```

**Step 6: Run tests**

Run: `cd ./memdb-go && go test ./internal/search/ -v`
Expected: PASS

**Step 7: Commit**

```bash
cd ./memdb-go
git add internal/search/service.go internal/search/config.go internal/search/internet_pipeline_test.go
git commit -m "feat(search): wire internet search into parallel pipeline

SearXNG results are embedded and merged with text_mem at InternetBaseScore=0.5.
Cosine rerank determines final ordering."
```

---

## Task 3: Add LLM Fine Mode (Enhance + Recall)

**Files:**
- Create: `internal/search/fine.go`
- Create: `internal/search/fine_test.go`

**Context:** Fine mode = fast search + LLM filtering (keep/drop each memory) + optional recall for gaps. Go already has `enhance.go` as a template for LLM calls. Fine mode reuses the same LLM infra (CLIProxyAPI, OpenAI-compatible).

The LLM call:
- Input: query + list of memories (id, text)
- Output: JSON array of {id, keep: bool, reason}
- Memories marked keep=false are dropped
- If >30% dropped, generate a "recall hint" query, search again, merge

**Step 1: Write tests**

```go
package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFineFilter_KeepsRelevant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": `[{"id":"1","keep":true},{"id":"2","keep":false}]`,
				},
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	memories := []map[string]any{
		{"id": "1", "memory": "User likes Go"},
		{"id": "2", "memory": "Random noise"},
	}

	cfg := FineConfig{APIURL: srv.URL, APIKey: "test", Model: "test"}
	kept := LLMFilter(context.Background(), "what does user like?", memories, cfg)
	if len(kept) != 1 {
		t.Fatalf("expected 1 kept, got %d", len(kept))
	}
	if kept[0]["id"] != "1" {
		t.Errorf("expected id=1, got %v", kept[0]["id"])
	}
}

func TestFineFilter_ReturnsAllOnLLMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	memories := []map[string]any{
		{"id": "1", "memory": "User likes Go"},
	}
	cfg := FineConfig{APIURL: srv.URL, APIKey: "test", Model: "test"}
	kept := LLMFilter(context.Background(), "query", memories, cfg)
	if len(kept) != 1 {
		t.Fatalf("expected all returned on error, got %d", len(kept))
	}
}
```

**Step 2: Run test, verify fail**

Run: `cd ./memdb-go && go test ./internal/search/ -run TestFine -v`

**Step 3: Implement fine.go**

```go
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	fineFilterTimeout = 15 * time.Second
	fineRecallThresh  = 0.30 // if >30% dropped, do recall search
	fineMaxMemories   = 20   // max memories to send to LLM
)

// FineConfig holds LLM connection info for fine-mode filtering.
type FineConfig struct {
	APIURL string
	APIKey string
	Model  string
}

// filterDecision is the LLM's per-memory keep/drop verdict.
type filterDecision struct {
	ID   string `json:"id"`
	Keep bool   `json:"keep"`
}

// LLMFilter sends memories to LLM for relevance filtering. Returns kept memories.
// On any error, returns all memories unchanged (graceful degradation).
func LLMFilter(ctx context.Context, query string, memories []map[string]any, cfg FineConfig) []map[string]any {
	if len(memories) == 0 || cfg.APIURL == "" {
		return memories
	}

	input := memories
	if len(input) > fineMaxMemories {
		input = input[:fineMaxMemories]
	}

	var sb strings.Builder
	for _, m := range input {
		id, _ := m["id"].(string)
		mem, _ := m["memory"].(string)
		if id == "" {
			if meta, ok := m["metadata"].(map[string]any); ok {
				id, _ = meta["id"].(string)
			}
		}
		fmt.Fprintf(&sb, "- [%s] %s\n", id, mem)
	}

	prompt := fmt.Sprintf(fineFilterPrompt, query, sb.String())

	body, err := callLLMForJSON(ctx, prompt, cfg)
	if err != nil {
		return memories
	}

	var decisions []filterDecision
	if err := json.Unmarshal(body, &decisions); err != nil {
		return memories
	}

	keepSet := make(map[string]bool, len(decisions))
	for _, d := range decisions {
		if d.Keep {
			keepSet[d.ID] = true
		}
	}

	kept := make([]map[string]any, 0, len(memories))
	for _, m := range memories {
		id := extractID(m)
		if keepSet[id] {
			kept = append(kept, m)
		}
	}

	// If nothing kept, return originals (LLM may have returned bad IDs)
	if len(kept) == 0 {
		return memories
	}
	return kept
}

// LLMRecallHint asks the LLM what information is missing and returns a hint query.
// Returns empty string on error.
func LLMRecallHint(ctx context.Context, query string, memories []map[string]any, cfg FineConfig) string {
	if cfg.APIURL == "" {
		return ""
	}

	var sb strings.Builder
	for _, m := range memories {
		mem, _ := m["memory"].(string)
		fmt.Fprintf(&sb, "- %s\n", mem)
	}

	prompt := fmt.Sprintf(fineRecallPrompt, query, sb.String())

	body, err := callLLMForJSON(ctx, prompt, cfg)
	if err != nil {
		return ""
	}

	var result struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return ""
	}
	return result.Query
}

func extractID(m map[string]any) string {
	if id, ok := m["id"].(string); ok && id != "" {
		return id
	}
	if meta, ok := m["metadata"].(map[string]any); ok {
		if id, ok := meta["id"].(string); ok {
			return id
		}
	}
	return ""
}

func callLLMForJSON(ctx context.Context, prompt string, cfg FineConfig) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fineFilterTimeout)
	defer cancel()

	reqBody := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature":    0.0,
		"response_format": map[string]string{"type": "json_object"},
	}
	encoded, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIURL+"/v1/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM returned %d", resp.StatusCode)
	}

	var llmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&llmResp); err != nil {
		return nil, err
	}
	if len(llmResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in LLM response")
	}

	content := strings.TrimSpace(llmResp.Choices[0].Message.Content)
	return []byte(content), nil
}

const fineFilterPrompt = `You are a memory relevance judge. Given a user query and retrieved memories, decide which memories are relevant.

Query: %s

Memories:
%s

Return a JSON array. Each element: {"id": "<memory_id>", "keep": true/false}
Only keep memories directly relevant to answering the query. Be strict — drop tangential results.`

const fineRecallPrompt = `Given a user query and the memories already retrieved, what important information might be missing?

Query: %s

Already retrieved:
%s

Return JSON: {"query": "<search query to find missing information>"}
If nothing is missing, return {"query": ""}.`
```

**Step 4: Run tests**

Run: `cd ./memdb-go && go test ./internal/search/ -run TestFine -v`
Expected: PASS

**Step 5: Commit**

```bash
cd ./memdb-go
git add internal/search/fine.go internal/search/fine_test.go
git commit -m "feat(search): add LLM fine-mode filter and recall hint

LLMFilter keeps/drops memories via LLM relevance judgment.
LLMRecallHint generates follow-up query when >30% dropped.
Graceful degradation on any LLM error."
```

---

## Task 4: Wire Fine Mode into SearchService

**Files:**
- Modify: `internal/search/service.go` — add fine mode orchestration
- Modify: `internal/search/config.go` — add `FineConfig` to `SearchService`

**Context:** Fine mode = run fast search → LLM filter → if >30% dropped, recall search → merge → return. This happens AFTER the existing pipeline (steps 1-12), replacing the post-process step.

**Step 1: Add FineConfig to SearchService struct**

In `service.go`, add to `SearchService`:
```go
Fine FineConfig // nil-ish = fine mode unavailable
```

**Step 2: Add fine mode orchestration method**

```go
// SearchFine runs fast search + LLM filtering + optional recall.
func (s *SearchService) SearchFine(ctx context.Context, p SearchParams) (*SearchOutput, error) {
	// Run the standard fast search first
	p.LLMRerank = true // fine always reranks
	output, err := s.Search(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.Fine.APIURL == "" || output.Result == nil {
		return output, nil
	}

	// Extract text memories for LLM filtering
	textMems := extractBucketMemories(output.Result.TextMem)
	if len(textMems) < 3 {
		return output, nil // too few to filter
	}

	// LLM filter
	kept := LLMFilter(ctx, p.Query, textMems, s.Fine)
	dropRate := 1.0 - float64(len(kept))/float64(len(textMems))

	// Recall for missing if >30% dropped
	if dropRate > fineRecallThresh && s.embedder != nil && s.postgres != nil {
		hint := LLMRecallHint(ctx, p.Query, kept, s.Fine)
		if hint != "" {
			recallResults := s.recallSearch(ctx, hint, p)
			kept = mergeRecallIntoKept(kept, recallResults, p.TopK)
		}
	}

	// Rebuild text_mem bucket
	output.Result.TextMem = []MemoryBucket{{
		CubeID:     p.CubeID,
		Memories:   TrimSlice(kept, p.TopK),
		TotalNodes: len(kept),
	}}

	return output, nil
}
```

Add helpers:
```go
func extractBucketMemories(buckets []MemoryBucket) []map[string]any {
	var all []map[string]any
	for _, b := range buckets {
		all = append(all, b.Memories...)
	}
	return all
}

func (s *SearchService) recallSearch(ctx context.Context, hint string, p SearchParams) []map[string]any {
	vecs, err := s.embedder.Embed(ctx, []string{hint})
	if err != nil || len(vecs) == 0 {
		return nil
	}
	results, err := s.postgres.VectorSearch(ctx, vecs[0], p.UserName, TextScopes, p.AgentID, p.TopK)
	if err != nil {
		return nil
	}
	merged := MergeVectorAndFulltext(results, nil)
	formatted, _ := FormatMergedItems(merged, false)
	return formatted
}

func mergeRecallIntoKept(kept, recall []map[string]any, limit int) []map[string]any {
	seen := make(map[string]bool, len(kept))
	for _, m := range kept {
		seen[extractID(m)] = true
	}
	for _, m := range recall {
		if len(kept) >= limit {
			break
		}
		if !seen[extractID(m)] {
			kept = append(kept, m)
			seen[extractID(m)] = true
		}
	}
	return kept
}
```

**Step 3: Run all search tests**

Run: `cd ./memdb-go && go test ./internal/search/ -v`
Expected: PASS

**Step 4: Commit**

```bash
cd ./memdb-go
git add internal/search/service.go internal/search/config.go
git commit -m "feat(search): wire fine mode into SearchService

Fine = fast search + LLM filter + recall for gaps.
Reuses existing Search pipeline, adds post-processing layer."
```

---

## Task 5: Remove Proxy Fallback from NativeSearch Handler

**Files:**
- Modify: `internal/handlers/search.go` — remove all `proxyWithBody` calls
- Modify: `internal/handlers/search.go` — route `mode=fine` to `SearchFine`
- Modify: `internal/handlers/search.go` — pass `internet_search` to params
- Modify: `internal/handlers/validate.go` — remove `ValidatedSearch` (dead code)
- Create: `internal/handlers/search_native_test.go`

**Context:** This is the key change. Currently `NativeSearch` has 3 proxy fallback paths. We remove all of them:
1. `searchService == nil` → return 503 (service unavailable)
2. `mode == "fine"` → call `searchService.SearchFine()`
3. `internet_search == true` → set `params.InternetSearch = true`
4. Native search error → return 500 (not proxy)

**Step 1: Write test for fine mode routing**

```go
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeSearch_FineModeNoProxy(t *testing.T) {
	// When mode=fine and searchService is nil, should return 503 not proxy
	h := NewHandler(nil, slog.Default())
	body := `{"query":"test","user_id":"user1","mode":"fine"}`
	req := httptest.NewRequest(http.MethodPost, "/product/search", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.NativeSearch(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}
```

**Step 2: Rewrite NativeSearch handler**

Replace the entire `NativeSearch` function:

```go
func (h *Handler) NativeSearch(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req searchRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if !h.checkErrors(w, validateSearchRequest(req)) {
		return
	}

	// Service must be available — no proxy fallback
	if h.searchService == nil || !h.searchService.CanSearch() {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "search service unavailable",
			"data":    nil,
		})
		return
	}

	params, err := buildSearchParams(req)
	if err != nil {
		h.writeValidationError(w, []string{err.Error()})
		return
	}

	// Internet search flag
	if req.InternetSearch != nil && *req.InternetSearch {
		params.InternetSearch = true
	}

	ctx := r.Context()

	// Check cache (same key for all modes)
	profileKey := derefStringOr(req.Profile, "default")
	modeKey := derefStringOr(req.Mode, "fast")
	cacheKey := fmt.Sprintf("%ssearch:%s:%s:%s:%s:%d:%d:%d:%s",
		cachePrefix, profileKey, modeKey, params.UserName,
		hashQuery(params.Query), params.TopK, params.SkillTopK, params.PrefTopK, params.Dedup)
	if cached := h.cacheGet(ctx, cacheKey); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cached)
		return
	}

	// Route by mode
	var output *search.SearchOutput
	if req.Mode != nil && *req.Mode == modeFine {
		output, err = h.searchService.SearchFine(ctx, params)
	} else {
		output, err = h.searchService.Search(ctx, params)
	}

	if err != nil {
		h.logger.Error("native search failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    500,
			"message": "search failed: " + err.Error(),
			"data":    nil,
		})
		return
	}

	resp := map[string]any{
		"code":    200,
		"message": "Search completed successfully",
		"data":    output.Result,
	}
	if encoded, err := json.Marshal(resp); err == nil {
		h.cacheSet(ctx, cacheKey, encoded, search.CacheTTL)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encoded)
	} else {
		h.writeJSON(w, http.StatusOK, resp)
	}

	h.logSearchResult(output.Result, params.Query, params.Dedup)
}
```

**Step 3: Delete ValidatedSearch from validate.go**

Remove the `ValidatedSearch` method entirely — it only proxied to Python and is no longer referenced.

**Step 4: Run tests**

Run: `cd ./memdb-go && go test ./internal/handlers/ -v`
Expected: PASS

**Step 5: Commit**

```bash
cd ./memdb-go
git add internal/handlers/search.go internal/handlers/validate.go internal/handlers/search_native_test.go
git commit -m "feat(search): remove proxy fallback, fully native /product/search

BREAKING: Go no longer proxies search to Python. Fine mode handled via
LLM filter. Internet search via SearXNG. 503 if service unavailable.
Eliminates Go→Python→Go circular dependency causing 30s timeouts."
```

---

## Task 6: Wire SearXNG and FineConfig in Server Init

**Files:**
- Modify: `internal/server/server.go` — initialize InternetSearcher and FineConfig
- Modify: `internal/config/config.go` — add SearXNG URL config

**Step 1: Add config field**

In `internal/config/config.go`, add:
```go
SearXNGURL string // SearXNG base URL (e.g., http://searxng:8080)
```

In the `Load()` or config init function, add:
```go
SearXNGURL: envStr("SEARXNG_URL", "http://searxng:8080"),
```

**Step 2: Wire in server.go**

In `initSearchService`, after creating `svc`, add:
```go
if cfg.SearXNGURL != "" {
	svc.Internet = search.NewInternetSearcher(cfg.SearXNGURL, search.DefaultInternetLimit)
	logger.Info("internet search enabled", slog.String("searxng_url", cfg.SearXNGURL))
}

if cfg.LLMProxyURL != "" {
	svc.Fine = search.FineConfig{
		APIURL: cfg.LLMProxyURL,
		APIKey: cfg.LLMProxyAPIKey,
		Model:  cfg.LLMSearchModel,
	}
	logger.Info("fine search mode enabled")
}
```

**Step 3: Add SEARXNG_URL to docker-compose.yml**

In `/home/krolik/deploy/krolik-server/docker-compose.yml`, add to `memdb-go` environment:
```yaml
SEARXNG_URL: http://searxng:8080
```

**Step 4: Test locally**

Run: `cd /home/krolik/deploy/krolik-server && docker compose build --no-cache memdb-go && docker compose up -d --no-deps --force-recreate memdb-go`
Check: `docker compose logs memdb-go --tail 20` — should see "internet search enabled" and "fine search mode enabled"

**Step 5: Commit**

```bash
cd ./memdb-go
git add internal/config/config.go internal/server/server.go
git commit -m "feat(search): wire SearXNG and fine config in server init"
```

Separately commit docker-compose:
```bash
cd /home/krolik/deploy/krolik-server
git add docker-compose.yml
git commit -m "feat: add SEARXNG_URL to memdb-go environment"
```

---

## Task 7: Integration Test — End-to-End Verification

**Files:**
- No new files — runtime verification

**Step 1: Deploy**

```bash
cd /home/krolik/deploy/krolik-server
docker compose build --no-cache memdb-go
docker compose up -d --no-deps --force-recreate memdb-go
```

**Step 2: Verify health**

```bash
curl http://127.0.0.1:8080/health
# Expected: {"status":"ok",...}
docker compose ps memdb-go
# Expected: healthy
```

**Step 3: Test fast mode search (regression)**

```bash
curl -s -X POST http://127.0.0.1:8080/product/search \
  -H "Content-Type: application/json" \
  -H "X-Service-Secret: $INTERNAL_SERVICE_SECRET" \
  -d '{"query":"test memory","user_id":"test_user","mode":"fast"}' | jq .code
# Expected: 200
```

**Step 4: Test fine mode (new)**

```bash
curl -s -X POST http://127.0.0.1:8080/product/search \
  -H "Content-Type: application/json" \
  -H "X-Service-Secret: $INTERNAL_SERVICE_SECRET" \
  -d '{"query":"what does user prefer","user_id":"test_user","mode":"fine"}' | jq .code
# Expected: 200
```

**Step 5: Test internet_search (new)**

```bash
curl -s -X POST http://127.0.0.1:8080/product/search \
  -H "Content-Type: application/json" \
  -H "X-Service-Secret: $INTERNAL_SERVICE_SECRET" \
  -d '{"query":"latest news","user_id":"test_user","internet_search":true}' | jq .code
# Expected: 200
```

**Step 6: Verify no circular dependency**

```bash
# This was the failing case — Python calling Go which called Python
docker compose logs memdb-go --tail 20 2>&1 | grep -i "proxy"
# Expected: NO proxy log lines for /product/search
docker compose logs memdb-api --tail 20 2>&1 | grep -i "timeout"
# Expected: NO new timeout errors
```

**Step 7: Check memdb-api health recovers**

```bash
docker compose ps memdb-api
# Expected: healthy (no more 30s timeouts)
```

---

## Task 8: Cleanup — Remove Python go_client Dependency for Search (Optional)

**Files:**
- Modify: `./src/memdb/multi_mem_cube/single_cube.py`
- Modify: `./src/memdb/clients/go_search_client.py`

**Context:** Now that Go never proxies search to Python, the circular path is broken. Python's `_fast_search` calling Go is safe. However, to fully decouple, we can make Python's `_fast_search` use its own direct DB path instead of calling Go. This eliminates the dependency entirely and makes Python standalone for search (useful if Go is down).

This is OPTIONAL — the circular dependency is already fixed by Task 5. Only do this if we want Python to be fully independent.

**Step 1: In single_cube.py `_fast_search`, remove go_client preference**

Change `_fast_search` to always use the direct DB path (lines 465-521), removing the `if self.go_client is not None:` block. Keep `go_client` for other uses (or mark for removal).

**Step 2: Deploy memdb-api**

```bash
cd /home/krolik/deploy/krolik-server
docker compose build --no-cache memdb-api
docker compose up -d --no-deps --force-recreate memdb-api
```

**Step 3: Verify both services healthy**

```bash
docker compose ps memdb-api memdb-go
# Both: healthy
```
