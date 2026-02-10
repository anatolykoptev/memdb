package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// openaiEmbeddingRequest is the OpenAI-compatible embedding request format.
type openaiEmbeddingRequest struct {
	Input json.RawMessage `json:"input"`
	Model string          `json:"model"`
}

// openaiEmbeddingResponse is the OpenAI-compatible embedding response format.
type openaiEmbeddingResponse struct {
	Object string                `json:"object"`
	Data   []openaiEmbeddingData `json:"data"`
	Model  string                `json:"model"`
	Usage  openaiEmbeddingUsage  `json:"usage"`
}

type openaiEmbeddingData struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type openaiEmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// openaiErrorResponse is the OpenAI-compatible error response format.
type openaiErrorResponse struct {
	Error openaiErrorDetail `json:"error"`
}

type openaiErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

const passagePrefix = "passage: "

// OpenAIEmbeddings handles POST /v1/embeddings with OpenAI-compatible request/response format.
// The Python MemDB backend uses UniversalAPIEmbedder (OpenAI SDK client) to call this endpoint.
func (h *Handler) OpenAIEmbeddings(w http.ResponseWriter, r *http.Request) {
	if h.embedder == nil {
		h.writeOpenAIError(w, http.StatusServiceUnavailable, "embedder not initialized", "server_error")
		return
	}

	var req openaiEmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeOpenAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error")
		return
	}

	// Parse input: can be a single string or an array of strings.
	texts, err := parseEmbeddingInput(req.Input)
	if err != nil {
		h.writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	if len(texts) == 0 {
		h.writeOpenAIError(w, http.StatusBadRequest, "input is required", "invalid_request_error")
		return
	}

	// Add "passage: " prefix for e5 models (storage/document use case).
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = passagePrefix + t
	}

	embeddings, err := h.embedder.Embed(r.Context(), prefixed)
	if err != nil {
		h.writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}

	// Build response.
	model := req.Model
	if model == "" {
		model = "multilingual-e5-large"
	}

	data := make([]openaiEmbeddingData, len(embeddings))
	for i, emb := range embeddings {
		data[i] = openaiEmbeddingData{
			Object:    "embedding",
			Embedding: emb,
			Index:     i,
		}
	}

	h.logger.Info("openai embeddings complete", slog.Int("texts", len(texts)))

	h.writeJSON(w, http.StatusOK, openaiEmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  model,
		Usage:  openaiEmbeddingUsage{PromptTokens: 0, TotalTokens: 0},
	})
}

// parseEmbeddingInput handles the polymorphic "input" field: string or []string.
func parseEmbeddingInput(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	// Try string first.
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if single == "" {
			return nil, nil
		}
		return []string{single}, nil
	}

	// Try array of strings.
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("input must be a string or array of strings")
	}
	return arr, nil
}

// writeOpenAIError writes an OpenAI-compatible error response.
func (h *Handler) writeOpenAIError(w http.ResponseWriter, status int, message, errType string) {
	h.writeJSON(w, status, openaiErrorResponse{
		Error: openaiErrorDetail{
			Message: message,
			Type:    errType,
			Code:    nil,
		},
	})
}
