package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
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

// modelPrefixes defines per-model text prefixes.
// e5 models need "passage: " prefix; other models get raw text.
var modelPrefixes = map[string]string{
	"multilingual-e5-large": "passage: ",
}

// OpenAIEmbeddings handles POST /v1/embeddings with OpenAI-compatible request/response format.
// Supports multi-model via embedRegistry (if set) or falls back to single embedder.
func (h *Handler) OpenAIEmbeddings(w http.ResponseWriter, r *http.Request) {
	var req openaiEmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeOpenAIError(w, http.StatusBadRequest, "invalid request body: "+err.Error(), "invalid_request_error")
		return
	}

	texts, err := parseEmbeddingInput(req.Input)
	if err != nil {
		h.writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if len(texts) == 0 {
		h.writeOpenAIError(w, http.StatusBadRequest, "input is required", "invalid_request_error")
		return
	}

	// Select embedder: registry (multi-model) or single embedder (legacy).
	var emb embedder.Embedder
	model := req.Model
	if h.embedRegistry != nil {
		var ok bool
		emb, ok = h.embedRegistry.Get(model)
		if !ok {
			h.writeOpenAIError(w, http.StatusBadRequest, "unknown model: "+model, "invalid_request_error")
			return
		}
	} else if h.embedder != nil {
		emb = h.embedder
	} else {
		h.writeOpenAIError(w, http.StatusServiceUnavailable, "embedder not initialized", "server_error")
		return
	}

	// Resolve display model name (fallback default).
	if model == "" {
		model = "multilingual-e5-large"
	}

	// Apply model-specific prefix and run embedder.
	input := applyModelPrefix(texts, model)
	embeddings, err := emb.Embed(r.Context(), input)
	if err != nil {
		h.writeOpenAIError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}

	data := make([]openaiEmbeddingData, len(embeddings))
	for i, vec := range embeddings {
		data[i] = openaiEmbeddingData{
			Object:    "embedding",
			Embedding: vec,
			Index:     i,
		}
	}

	h.logger.Info("openai embeddings complete", slog.Int("texts", len(texts)), slog.String("model", model))

	h.writeJSON(w, http.StatusOK, openaiEmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  model,
		Usage:  openaiEmbeddingUsage{PromptTokens: 0, TotalTokens: 0},
	})
}

// applyModelPrefix adds model-specific prefix to texts.
// e5 models need "passage: " prefix; other models get raw text.
func applyModelPrefix(texts []string, model string) []string {
	prefix, ok := modelPrefixes[model]
	if !ok && strings.Contains(model, "e5") {
		prefix = "passage: "
	}
	if prefix == "" {
		return texts
	}
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = prefix + t
	}
	return prefixed
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
		return nil, errors.New("input must be a string or array of strings")
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
