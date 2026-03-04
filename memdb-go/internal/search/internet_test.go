package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternetSearch_ParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Error("expected format=json query param")
		}
		if r.URL.Query().Get("categories") != "general" {
			t.Error("expected categories=general query param")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"First","content":"snippet one","url":"https://example.com/1"},
			{"title":"Second","content":"snippet two","url":"https://example.com/2"}
		]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(srv.URL, 10)
	results, err := s.Search(context.Background(), "test query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Title != "First" || results[0].Content != "snippet one" || results[0].URL != "https://example.com/1" {
		t.Errorf("unexpected first result: %+v", results[0])
	}
	if results[1].Title != "Second" {
		t.Errorf("unexpected second result title: %s", results[1].Title)
	}
	want := "First: snippet one"
	if got := results[0].Text(); got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestInternetSearch_EmptyOnError(t *testing.T) {
	s := NewInternetSearcher("http://127.0.0.1:1", 10) // unreachable
	results, err := s.Search(context.Background(), "test")
	if err != nil {
		t.Fatalf("expected no error on failure, got: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty slice, got %d results", len(results))
	}
}

func TestInternetSearch_LimitsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"A","content":"a","url":"https://a.com"},
			{"title":"B","content":"b","url":"https://b.com"},
			{"title":"C","content":"c","url":"https://c.com"}
		]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(srv.URL, 2)
	results, err := s.Search(context.Background(), "q")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (limit), got %d", len(results))
	}
}

func TestInternetSearch_SkipsEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"","content":"","url":"https://empty.com"},
			{"title":"Valid","content":"ok","url":"https://valid.com"}
		]}`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(srv.URL, 10)
	results, err := s.Search(context.Background(), "q")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (skip empty), got %d", len(results))
	}
	if results[0].Title != "Valid" {
		t.Errorf("expected Valid, got %s", results[0].Title)
	}
}

func TestInternetSearch_EmptyOnBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	s := NewInternetSearcher(srv.URL, 10)
	results, err := s.Search(context.Background(), "q")
	if err != nil {
		t.Fatalf("expected no error on bad JSON, got: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty slice on bad JSON, got %d", len(results))
	}
}

func TestInternetSearch_EmptyOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := NewInternetSearcher(srv.URL, 10)
	results, err := s.Search(context.Background(), "q")
	if err != nil {
		t.Fatalf("expected no error on 500, got: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty slice on 500, got %d", len(results))
	}
}
