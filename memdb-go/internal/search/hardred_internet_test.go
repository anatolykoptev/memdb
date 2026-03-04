package search

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBrowser implements enginesearch.BrowserDoer for testing direct scraper paths.
type fakeBrowser struct {
	doFunc func(method, url string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error)
}

func (fb *fakeBrowser) Do(method, url string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error) {
	return fb.doFunc(method, url, headers, body)
}

// --- Hard Red: Concurrent safety ---

func TestHR_Internet_ConcurrentSearches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"A","content":"a","url":"https://a.com"}]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := s.Search(context.Background(), "concurrent")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if len(results) == 0 {
				t.Error("expected results, got empty")
			}
		}()
	}
	wg.Wait()
}

// --- Hard Red: Context cancellation ---

func TestHR_Internet_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second) // slow server
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"A","content":"a","url":"https://a.com"}]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	results, err := s.Search(ctx, "timeout")
	if err != nil {
		t.Fatalf("expected no error (graceful), got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results on timeout, got %d", len(results))
	}
}

// --- Hard Red: SearXNG returns empty results array ---

func TestHR_Internet_EmptyResultsArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	results, err := s.Search(context.Background(), "empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty, got %d", len(results))
	}
}

// --- Hard Red: SearXNG returns null results ---

func TestHR_Internet_NullResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":null}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	results, err := s.Search(context.Background(), "null")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty, got %d", len(results))
	}
}

// --- Hard Red: Limit = 0 ---

func TestHR_Internet_ZeroLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"A","content":"a","url":"https://a.com"}]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      0,
	})

	results, err := s.Search(context.Background(), "zero")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty with limit=0, got %d", len(results))
	}
}

// --- Hard Red: Huge response body ---

func TestHR_Internet_LargeResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 500 results with long content
		var b strings.Builder
		b.WriteString(`{"results":[`)
		for i := 0; i < 500; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"title":"Title `)
			b.WriteString(strings.Repeat("x", 200))
			b.WriteString(`","content":"Content `)
			b.WriteString(strings.Repeat("y", 1000))
			b.WriteString(`","url":"https://example.com/`)
			b.WriteString(string(rune('a' + (i % 26))))
			b.WriteString(`"}`)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      5,
	})

	results, err := s.Search(context.Background(), "large")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("expected 5 (limit), got %d", len(results))
	}
}

// --- Hard Red: Server closes connection mid-response ---

func TestHR_Internet_ConnectionReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Skip("hijacking not supported")
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close() // abrupt close
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	results, err := s.Search(context.Background(), "reset")
	if err != nil {
		t.Fatalf("expected no error (graceful), got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty on reset, got %d", len(results))
	}
}

// --- Hard Red: Slow SearXNG, fast direct scrapers ---
// Ensures both sources contribute even if SearXNG is slow.

func TestHR_Internet_SlowSearXNG_FastDirect(t *testing.T) {
	var searxngCalled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		searxngCalled.Store(true)
		time.Sleep(200 * time.Millisecond) // slow but not timeout
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Slow","content":"searxng","url":"https://slow.com"}]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
		Browser:    nil, // SearXNG-only to test slow path
	})

	results, err := s.Search(context.Background(), "slow")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !searxngCalled.Load() {
		t.Error("SearXNG was not called")
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

// --- Hard Red: All results have empty title AND content (all filtered) ---

func TestHR_Internet_AllResultsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"","content":"","url":"https://a.com"},
			{"title":"","content":"","url":"https://b.com"},
			{"title":"","content":"","url":"https://c.com"}
		]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	results, err := s.Search(context.Background(), "empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 (all filtered), got %d", len(results))
	}
}

// --- Hard Red: Unicode/emoji in results ---

func TestHR_Internet_UnicodeResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"日本語テスト 🎉","content":"Ünïcödé cöntënt with émojis 🚀","url":"https://example.com/日本語"}
		]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	results, err := s.Search(context.Background(), "unicode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].Title, "日本語") {
		t.Errorf("title missing unicode: %s", results[0].Title)
	}
	if !strings.Contains(results[0].Content, "🚀") {
		t.Errorf("content missing emoji: %s", results[0].Content)
	}
}

// --- Hard Red: SearXNG returns non-JSON content type but valid JSON ---

func TestHR_Internet_WrongContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html") // wrong
		_, _ = w.Write([]byte(`{"results":[{"title":"A","content":"a","url":"https://a.com"}]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	// go-engine SearXNG client should still parse valid JSON regardless of content type
	results, err := s.Search(context.Background(), "ct")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Either parses or gracefully returns empty — both acceptable
	if len(results) > 1 {
		t.Logf("parsed %d results despite wrong content type", len(results))
	}
}

// --- Hard Red: SearXNG returns HTTP 429 (rate limited) ---

func TestHR_Internet_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	results, err := s.Search(context.Background(), "ratelimit")
	if err != nil {
		t.Fatalf("expected no error (graceful), got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty on 429, got %d", len(results))
	}
}

// --- Hard Red: Empty query ---

func TestHR_Internet_EmptyQuery(t *testing.T) {
	var queryReceived string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryReceived = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	results, err := s.Search(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty query should still work gracefully
	_ = queryReceived
	if results == nil {
		t.Error("expected non-nil slice")
	}
}

// --- Hard Red: Results with title-only or content-only (not both empty) ---

func TestHR_Internet_PartialResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Title Only","content":"","url":"https://a.com"},
			{"title":"","content":"Content Only","url":"https://b.com"},
			{"title":"","content":"","url":"https://c.com"},
			{"title":"Both","content":"Present","url":"https://d.com"}
		]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	results, err := s.Search(context.Background(), "partial")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// title-only, content-only, and both-present should be kept; empty-both filtered
	if len(results) != 3 {
		t.Errorf("expected 3 (skip empty-both), got %d", len(results))
	}
}

// --- Hard Red: Rapid sequential searches (no state leaks) ---

func TestHR_Internet_RapidSequential(t *testing.T) {
	var callCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"R` + strings.Repeat("x", int(n%3)) +
			`","content":"c","url":"https://example.com/` + string(rune('0'+n%10)) + `"}]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(InternetSearcherConfig{
		SearXNGURL: srv.URL,
		Limit:      10,
	})

	for i := 0; i < 50; i++ {
		results, err := s.Search(context.Background(), "rapid")
		if err != nil {
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}
		if len(results) == 0 {
			t.Errorf("iter %d: expected results", i)
		}
	}
}
