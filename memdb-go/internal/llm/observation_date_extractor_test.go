package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockObsDateLLM creates a test server that returns a fixed obsDateResponse JSON
// wrapped in the OpenAI chat-completion envelope.
func mockObsDateLLM(t *testing.T, date string) (*httptest.Server, *ObsDateExtractor) {
	t.Helper()
	resp := obsDateResponse{Date: date}
	body, _ := json.Marshal(resp)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": string(body)}},
			},
		})
	}))
	c := NewClient(srv.URL, "", "gemini-2.0-flash-lite", nil, nil)
	return srv, NewObsDateExtractor(c)
}

// TestExtractObservationDate_AbsoluteDate verifies that an explicit calendar
// date in the message text is returned as YYYY-MM-DD.
func TestExtractObservationDate_AbsoluteDate(t *testing.T) {
	srv, ext := mockObsDateLLM(t, "2024-01-06")
	defer srv.Close()

	got, err := ext.ExtractObservationDate(
		context.Background(),
		[]string{"On Jan 6 2024 we met for lunch."},
		"2024-01-08",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "2024-01-06" {
		t.Errorf("ExtractObservationDate = %q, want %q", got, "2024-01-06")
	}
}

// TestExtractObservationDate_RelativeYesterday verifies that a relative
// "yesterday" expression is resolved to ref_date - 1 by the LLM, and the
// result passes ISO validation.
func TestExtractObservationDate_RelativeYesterday(t *testing.T) {
	// LLM resolves "yesterday" against referenceDate "2024-01-08" → "2024-01-07".
	srv, ext := mockObsDateLLM(t, "2024-01-07")
	defer srv.Close()

	got, err := ext.ExtractObservationDate(
		context.Background(),
		[]string{"Yesterday I finished the project."},
		"2024-01-08",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "2024-01-07" {
		t.Errorf("ExtractObservationDate = %q, want %q", got, "2024-01-07")
	}
}

// TestExtractObservationDate_NoDate verifies that a message with no temporal
// anchor returns "" without error. Empty is valid — no forced date.
func TestExtractObservationDate_NoDate(t *testing.T) {
	srv, ext := mockObsDateLLM(t, "")
	defer srv.Close()

	got, err := ext.ExtractObservationDate(
		context.Background(),
		[]string{"I love pizza."},
		"2024-01-08",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("ExtractObservationDate = %q, want empty string for no-date message", got)
	}
}

// TestExtractObservationDate_PartialYear verifies that a year-only anchor
// ("in 2023") is approximated to the first day of the year (2023-01-01).
// The LLM performs the resolution; the extractor validates the ISO format.
func TestExtractObservationDate_PartialYear(t *testing.T) {
	// LLM resolves "in 2023" to 2023-01-01 (year-start approximation).
	srv, ext := mockObsDateLLM(t, "2023-01-01")
	defer srv.Close()

	got, err := ext.ExtractObservationDate(
		context.Background(),
		[]string{"In 2023 I started learning Go."},
		"2024-01-08",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "2023-01-01" {
		t.Errorf("ExtractObservationDate = %q, want %q", got, "2023-01-01")
	}
}

// TestExtractObservationDate_NonISODropped verifies that a non-ISO response
// from the LLM (e.g. "summer 2023") is silently dropped — returns "" —
// and the confidence-drop counter fires (no panic).
func TestExtractObservationDate_NonISODropped(t *testing.T) {
	srv, ext := mockObsDateLLM(t, "summer 2023")
	defer srv.Close()

	got, err := ext.ExtractObservationDate(
		context.Background(),
		[]string{"We met last summer."},
		"2023-09-01",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("ExtractObservationDate with non-ISO response = %q, want empty (dropped)", got)
	}
}

// TestExtractObservationDate_NilExtractor verifies that a nil receiver returns
// an empty string gracefully without panicking.
func TestExtractObservationDate_NilExtractor(t *testing.T) {
	var ext *ObsDateExtractor
	got, err := ext.ExtractObservationDate(context.Background(), []string{"hello"}, "2024-01-01")
	if err != nil {
		t.Fatalf("nil receiver: unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("nil receiver: got %q, want empty", got)
	}
}

// TestExtractObservationDate_EmptyMessages verifies that passing no messages
// returns "" without making an LLM call.
func TestExtractObservationDate_EmptyMessages(t *testing.T) {
	// No server needed — should return early without a call.
	c := NewClient("http://127.0.0.1:1", "", "model", nil, nil)
	ext := NewObsDateExtractor(c)

	got, err := ext.ExtractObservationDate(context.Background(), nil, "2024-01-01")
	if err != nil {
		t.Fatalf("empty messages: unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("empty messages: got %q, want empty", got)
	}
}
