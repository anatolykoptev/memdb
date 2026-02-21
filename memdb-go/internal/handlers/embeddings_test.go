package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockEmbedder implements embedder.Embedder for testing the embeddings handler.
type mockEmbedder struct {
	embedFn func(ctx context.Context, texts []string) ([][]float32, error)
	dim     int
}

func (m *mockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return m.embedFn(ctx, texts)
}

func (m *mockEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vecs, err := m.embedFn(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		return nil, err
	}
	return vecs[0], nil
}

func (m *mockEmbedder) Dimension() int {
	if m.dim > 0 {
		return m.dim
	}
	return 1024
}

func (m *mockEmbedder) Close() error { return nil }

func TestOpenAIEmbeddings_Success_SingleString(t *testing.T) {
	h := &Handler{
		logger: discardLogger(),
		embedder: &mockEmbedder{
			embedFn: func(ctx context.Context, texts []string) ([][]float32, error) {
				// Verify "passage: " prefix is added by the handler.
				for _, text := range texts {
					if !strings.HasPrefix(text, "passage: ") {
						t.Errorf("expected passage prefix, got %q", text)
					}
				}
				result := make([][]float32, len(texts))
				for i := range texts {
					result[i] = []float32{0.1, 0.2, 0.3}
				}
				return result, nil
			},
		},
	}

	body := `{"input": "hello world", "model": "test-model"}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp openaiEmbeddingResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("object=%q, want list", resp.Object)
	}
	if resp.Model != "test-model" {
		t.Errorf("model=%q, want test-model", resp.Model)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data len=%d, want 1", len(resp.Data))
	}
	if resp.Data[0].Object != "embedding" {
		t.Errorf("data[0].object=%q, want embedding", resp.Data[0].Object)
	}
	if resp.Data[0].Index != 0 {
		t.Errorf("data[0].index=%d, want 0", resp.Data[0].Index)
	}
	if len(resp.Data[0].Embedding) != 3 {
		t.Errorf("embedding len=%d, want 3", len(resp.Data[0].Embedding))
	}
	// Verify actual values.
	want := []float32{0.1, 0.2, 0.3}
	for i, v := range resp.Data[0].Embedding {
		if v != want[i] {
			t.Errorf("embedding[%d]=%f, want %f", i, v, want[i])
		}
	}
}

func TestOpenAIEmbeddings_Success_ArrayInput(t *testing.T) {
	callCount := 0
	h := &Handler{
		logger: discardLogger(),
		embedder: &mockEmbedder{
			embedFn: func(ctx context.Context, texts []string) ([][]float32, error) {
				callCount++
				if len(texts) != 2 {
					t.Errorf("expected 2 texts, got %d", len(texts))
				}
				return [][]float32{{0.1}, {0.2}}, nil
			},
		},
	}

	body := `{"input": ["text1", "text2"]}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp openaiEmbeddingResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data len=%d, want 2", len(resp.Data))
	}
	if resp.Data[0].Index != 0 {
		t.Errorf("data[0].index=%d, want 0", resp.Data[0].Index)
	}
	if resp.Data[1].Index != 1 {
		t.Errorf("data[1].index=%d, want 1", resp.Data[1].Index)
	}
	// When model is empty in the request, default is "multilingual-e5-large".
	if resp.Model != "multilingual-e5-large" {
		t.Errorf("model=%q, want multilingual-e5-large", resp.Model)
	}
	if callCount != 1 {
		t.Errorf("embedFn called %d times, want 1", callCount)
	}
}

func TestOpenAIEmbeddings_NilEmbedder(t *testing.T) {
	h := &Handler{logger: discardLogger()} // embedder is nil

	body := `{"input": "test"}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	if w.Code != 503 {
		t.Fatalf("expected 503, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp openaiErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Type != "server_error" {
		t.Errorf("type=%q, want server_error", resp.Error.Type)
	}
	if !strings.Contains(resp.Error.Message, "embedder") {
		t.Errorf("message=%q, expected it to mention embedder", resp.Error.Message)
	}
}

func TestOpenAIEmbeddings_EmptyArrayInput(t *testing.T) {
	h := &Handler{
		logger: discardLogger(),
		embedder: &mockEmbedder{embedFn: func(ctx context.Context, texts []string) ([][]float32, error) {
			t.Fatal("embedder should not be called for empty input")
			return nil, nil
		}},
	}

	body := `{"input": []}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp openaiErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Type != "invalid_request_error" {
		t.Errorf("type=%q, want invalid_request_error", resp.Error.Type)
	}
}

func TestOpenAIEmbeddings_EmptyStringInput(t *testing.T) {
	h := &Handler{
		logger: discardLogger(),
		embedder: &mockEmbedder{embedFn: func(ctx context.Context, texts []string) ([][]float32, error) {
			t.Fatal("embedder should not be called for empty string input")
			return nil, nil
		}},
	}

	body := `{"input": ""}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	// Empty string parses to nil via parseEmbeddingInput, then len(texts)==0 returns 400.
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestOpenAIEmbeddings_InvalidBody(t *testing.T) {
	h := &Handler{
		logger: discardLogger(),
		embedder: &mockEmbedder{embedFn: func(ctx context.Context, texts []string) ([][]float32, error) {
			t.Fatal("embedder should not be called for invalid body")
			return nil, nil
		}},
	}

	body := `not json`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp openaiErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Type != "invalid_request_error" {
		t.Errorf("type=%q, want invalid_request_error", resp.Error.Type)
	}
}

func TestOpenAIEmbeddings_InvalidInputType(t *testing.T) {
	h := &Handler{
		logger: discardLogger(),
		embedder: &mockEmbedder{embedFn: func(ctx context.Context, texts []string) ([][]float32, error) {
			return nil, nil
		}},
	}

	// Input is a number, which is neither string nor []string.
	body := `{"input": 42}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d; body: %s", w.Code, w.Body.String())
	}
}

func TestOpenAIEmbeddings_EmbedderError(t *testing.T) {
	h := &Handler{
		logger: discardLogger(),
		embedder: &mockEmbedder{
			embedFn: func(ctx context.Context, texts []string) ([][]float32, error) {
				return nil, fmt.Errorf("voyage: status 429: rate limited")
			},
		},
	}

	body := `{"input": "test"}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	if w.Code != 500 {
		t.Fatalf("expected 500, got %d; body: %s", w.Code, w.Body.String())
	}

	var resp openaiErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Type != "server_error" {
		t.Errorf("type=%q, want server_error", resp.Error.Type)
	}
	if !strings.Contains(resp.Error.Message, "429") {
		t.Errorf("message=%q, expected it to contain 429", resp.Error.Message)
	}
}

func TestOpenAIEmbeddings_PassagePrefix(t *testing.T) {
	// Verify that each text sent to the embedder has "passage: " prepended.
	var capturedTexts []string
	h := &Handler{
		logger: discardLogger(),
		embedder: &mockEmbedder{
			embedFn: func(ctx context.Context, texts []string) ([][]float32, error) {
				capturedTexts = texts
				result := make([][]float32, len(texts))
				for i := range texts {
					result[i] = []float32{0.5}
				}
				return result, nil
			},
		},
	}

	body := `{"input": ["alpha", "beta"]}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(capturedTexts) != 2 {
		t.Fatalf("expected 2 texts sent to embedder, got %d", len(capturedTexts))
	}
	if capturedTexts[0] != "passage: alpha" {
		t.Errorf("texts[0]=%q, want %q", capturedTexts[0], "passage: alpha")
	}
	if capturedTexts[1] != "passage: beta" {
		t.Errorf("texts[1]=%q, want %q", capturedTexts[1], "passage: beta")
	}
}

func TestOpenAIEmbeddings_UsageField(t *testing.T) {
	h := &Handler{
		logger: discardLogger(),
		embedder: &mockEmbedder{
			embedFn: func(ctx context.Context, texts []string) ([][]float32, error) {
				return [][]float32{{0.1}}, nil
			},
		},
	}

	body := `{"input": "test"}`
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.OpenAIEmbeddings(w, req)

	var resp openaiEmbeddingResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Usage is always 0 for local/proxy embeddings (token count not tracked).
	if resp.Usage.PromptTokens != 0 {
		t.Errorf("prompt_tokens=%d, want 0", resp.Usage.PromptTokens)
	}
	if resp.Usage.TotalTokens != 0 {
		t.Errorf("total_tokens=%d, want 0", resp.Usage.TotalTokens)
	}
}

// --- parseEmbeddingInput unit tests ---

func TestParseEmbeddingInput_String(t *testing.T) {
	raw := json.RawMessage(`"hello"`)
	texts, err := parseEmbeddingInput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(texts) != 1 || texts[0] != "hello" {
		t.Errorf("got %v, want [hello]", texts)
	}
}

func TestParseEmbeddingInput_Array(t *testing.T) {
	raw := json.RawMessage(`["a", "b"]`)
	texts, err := parseEmbeddingInput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(texts) != 2 {
		t.Fatalf("got %d texts, want 2", len(texts))
	}
	if texts[0] != "a" || texts[1] != "b" {
		t.Errorf("got %v, want [a b]", texts)
	}
}

func TestParseEmbeddingInput_Nil(t *testing.T) {
	texts, err := parseEmbeddingInput(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if texts != nil {
		t.Errorf("expected nil, got %v", texts)
	}
}

func TestParseEmbeddingInput_EmptyRaw(t *testing.T) {
	texts, err := parseEmbeddingInput(json.RawMessage(``))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if texts != nil {
		t.Errorf("expected nil, got %v", texts)
	}
}

func TestParseEmbeddingInput_EmptyString(t *testing.T) {
	raw := json.RawMessage(`""`)
	texts, err := parseEmbeddingInput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if texts != nil {
		t.Errorf("expected nil for empty string, got %v", texts)
	}
}

func TestParseEmbeddingInput_InvalidType(t *testing.T) {
	raw := json.RawMessage(`42`)
	_, err := parseEmbeddingInput(raw)
	if err == nil {
		t.Fatal("expected error for numeric input")
	}
	if !strings.Contains(err.Error(), "string or array") {
		t.Errorf("error=%q, expected it to mention 'string or array'", err)
	}
}

func TestParseEmbeddingInput_SingleElementArray(t *testing.T) {
	raw := json.RawMessage(`["single"]`)
	texts, err := parseEmbeddingInput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(texts) != 1 || texts[0] != "single" {
		t.Errorf("got %v, want [single]", texts)
	}
}
