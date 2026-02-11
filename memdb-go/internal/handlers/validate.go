// Package handlers — request validation for typed endpoints.
// Validates request bodies against OpenAPI-generated types before proxying to Python.
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// --- Typed request structs for validation ---
// These mirror the generated OpenAPI types but only include fields we validate.
// Using separate structs avoids coupling to oapi-codegen union types that are
// difficult to unmarshal directly.

// searchRequest validates POST /product/search.
type searchRequest struct {
	Query  *string `json:"query"`
	UserID *string `json:"user_id"`
	TopK   *int    `json:"top_k,omitempty"`
	Dedup  *string `json:"dedup,omitempty"`

	Relativity   *float64 `json:"relativity,omitempty"`
	PrefTopK     *int     `json:"pref_top_k,omitempty"`
	ToolMemTopK  *int     `json:"tool_mem_top_k,omitempty"`
	SkillMemTopK *int     `json:"skill_mem_top_k,omitempty"`

	// Fields for native search handler proxy-fallback decisions
	Mode             *string   `json:"mode,omitempty"`
	InternetSearch   *bool     `json:"internet_search,omitempty"`
	ReadableCubeIDs  *[]string `json:"readable_cube_ids,omitempty"`
	IncludeEmbedding *bool     `json:"include_embedding,omitempty"`

	// Per-type gating
	IncludeSkillMemory *bool `json:"include_skill_memory,omitempty"`
	IncludePreference  *bool `json:"include_preference,omitempty"`
	SearchToolMemory   *bool `json:"search_tool_memory,omitempty"`
}

// addRequest validates POST /product/add.
type addRequest struct {
	UserID    *string `json:"user_id"`
	AsyncMode *string `json:"async_mode,omitempty"`
	Mode      *string `json:"mode,omitempty"`
}

// feedbackRequest validates POST /product/feedback.
type feedbackRequest struct {
	UserID          *string `json:"user_id"`
	FeedbackContent *string `json:"feedback_content"`
	History         *json.RawMessage `json:"history"`
}

// deleteRequest validates POST /product/delete_memory.
type deleteRequest struct {
	MemoryIDs *[]string              `json:"memory_ids"`
	FileIDs   *[]string              `json:"file_ids"`
	Filter    map[string]interface{} `json:"filter"`
}

// getAllRequest validates POST /product/get_all.
type getAllRequest struct {
	UserID     *string `json:"user_id"`
	MemoryType *string `json:"memory_type"`
}

// chatCompleteRequest validates POST /product/chat/complete.
type chatCompleteRequest struct {
	UserID *string `json:"user_id"`
	Query  *string `json:"query"`
	TopK   *int    `json:"top_k,omitempty"`
}

// chatRequest validates POST /product/chat and POST /product/chat/stream.
type chatRequest struct {
	UserID *string `json:"user_id"`
	Query  *string `json:"query"`
}

// getMemoryRequest validates POST /product/get_memory.
type getMemoryRequest struct {
	MemCubeID          *string                `json:"mem_cube_id"`
	UserID             *string                `json:"user_id,omitempty"`
	IncludePreference  *bool                  `json:"include_preference,omitempty"`
	IncludeToolMemory  *bool                  `json:"include_tool_memory,omitempty"`
	IncludeSkillMemory *bool                  `json:"include_skill_memory,omitempty"`
	Filter             map[string]interface{} `json:"filter,omitempty"`
	Page               *int                   `json:"page,omitempty"`
	PageSize           *int                   `json:"page_size,omitempty"`
}

// getMemoryByIDsRequest validates POST /product/get_memory_by_ids.
type getMemoryByIDsRequest struct {
	MemoryIDs *[]string `json:"memory_ids"`
}

// existMemCubeRequest validates POST /product/exist_mem_cube_id.
type existMemCubeRequest struct {
	MemCubeID *string `json:"mem_cube_id"`
}

// --- Validated handler methods ---

// ValidatedSearch validates and proxies POST /product/search.
func (h *Handler) ValidatedSearch(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req searchRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	var errs []string
	if req.Query == nil || strings.TrimSpace(*req.Query) == "" {
		errs = append(errs, "query is required and must be non-empty")
	}
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.TopK != nil && *req.TopK < 1 {
		errs = append(errs, "top_k must be >= 1")
	}
	if req.Dedup != nil {
		switch *req.Dedup {
		case "no", "sim", "mmr":
		default:
			errs = append(errs, "dedup must be one of: no, sim, mmr")
		}
	}
	if req.Relativity != nil && *req.Relativity < 0 {
		errs = append(errs, "relativity must be >= 0")
	}
	if req.PrefTopK != nil && *req.PrefTopK < 0 {
		errs = append(errs, "pref_top_k must be >= 0")
	}
	if req.ToolMemTopK != nil && *req.ToolMemTopK < 0 {
		errs = append(errs, "tool_mem_top_k must be >= 0")
	}
	if req.SkillMemTopK != nil && *req.SkillMemTopK < 0 {
		errs = append(errs, "skill_mem_top_k must be >= 0")
	}

	if !h.checkErrors(w, errs) {
		return
	}

	h.proxyWithBody(w, r, normalizeSearch(body))
}

// ValidatedAdd validates and proxies POST /product/add.
func (h *Handler) ValidatedAdd(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req addRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	var errs []string
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.AsyncMode != nil {
		switch *req.AsyncMode {
		case "async", "sync":
		default:
			errs = append(errs, "async_mode must be one of: async, sync")
		}
	}
	if req.Mode != nil {
		switch *req.Mode {
		case "fast", "fine":
		default:
			errs = append(errs, "mode must be one of: fast, fine")
		}
	}

	if !h.checkErrors(w, errs) {
		return
	}

	h.proxyWithBody(w, r, normalizeAdd(body))
}

// ValidatedFeedback validates and proxies POST /product/feedback.
func (h *Handler) ValidatedFeedback(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req feedbackRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	var errs []string
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.FeedbackContent == nil || *req.FeedbackContent == "" {
		errs = append(errs, "feedback_content is required")
	}
	if req.History == nil {
		errs = append(errs, "history is required")
	}

	if !h.checkErrors(w, errs) {
		return
	}

	h.proxyWithBody(w, r, normalizeFeedback(body))
}

// ValidatedDelete validates and proxies POST /product/delete_memory.
func (h *Handler) ValidatedDelete(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req deleteRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	hasMemoryIDs := req.MemoryIDs != nil && len(*req.MemoryIDs) > 0
	hasFileIDs := req.FileIDs != nil && len(*req.FileIDs) > 0
	hasFilter := len(req.Filter) > 0

	if !hasMemoryIDs && !hasFileIDs && !hasFilter {
		h.writeValidationError(w, []string{"at least one of memory_ids, file_ids, or filter is required"})
		return
	}

	h.proxyWithBody(w, r, body)
}

// ValidatedGetAll validates and proxies POST /product/get_all.
func (h *Handler) ValidatedGetAll(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req getAllRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	errs := validateGetAllRequest(req.UserID, req.MemoryType)
	if !h.checkErrors(w, errs) {
		return
	}

	h.proxyWithBody(w, r, body)
}

// ValidatedChatComplete validates and proxies POST /product/chat/complete.
func (h *Handler) ValidatedChatComplete(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req chatCompleteRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	var errs []string
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.Query == nil || strings.TrimSpace(*req.Query) == "" {
		errs = append(errs, "query is required and must be non-empty")
	}
	if req.TopK != nil && *req.TopK < 1 {
		errs = append(errs, "top_k must be >= 1")
	}

	if !h.checkErrors(w, errs) {
		return
	}

	h.proxyWithBody(w, r, normalizeChatComplete(body))
}

// ValidatedChat validates and proxies POST /product/chat.
func (h *Handler) ValidatedChat(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req chatRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	var errs []string
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	if req.Query == nil || strings.TrimSpace(*req.Query) == "" {
		errs = append(errs, "query is required and must be non-empty")
	}

	if !h.checkErrors(w, errs) {
		return
	}

	h.proxyWithBody(w, r, normalizeChatComplete(body))
}

// ValidatedChatStream validates and proxies POST /product/chat/stream.
func (h *Handler) ValidatedChatStream(w http.ResponseWriter, r *http.Request) {
	// Same validation as chat
	h.ValidatedChat(w, r)
}

// ValidatedGetMemory validates and proxies POST /product/get_memory.
func (h *Handler) ValidatedGetMemory(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req getMemoryRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	// mem_cube_id is required per Python model
	// but the endpoint also has a GET /product/get_memory/{memory_id} variant
	// POST variant needs at least a body
	h.proxyWithBody(w, r, body)
}

// ValidatedGetMemoryByIDs validates and proxies POST /product/get_memory_by_ids.
func (h *Handler) ValidatedGetMemoryByIDs(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req getMemoryByIDsRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if req.MemoryIDs == nil || len(*req.MemoryIDs) == 0 {
		h.writeValidationError(w, []string{"memory_ids is required and must be non-empty"})
		return
	}

	h.proxyWithBody(w, r, body)
}

// ValidatedExistMemCube validates and proxies POST /product/exist_mem_cube_id.
func (h *Handler) ValidatedExistMemCube(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}

	var req existMemCubeRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}

	if req.MemCubeID == nil || *req.MemCubeID == "" {
		h.writeValidationError(w, []string{"mem_cube_id is required"})
		return
	}

	h.proxyWithBody(w, r, body)
}

// --- Helpers ---

// readBody reads the full request body and returns it. Returns false on error.
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		h.writeValidationError(w, []string{"request body is required"})
		return nil, false
	}

	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		h.logger.Error("failed to read request body", slog.Any("error", err))
		h.writeValidationError(w, []string{"failed to read request body"})
		return nil, false
	}

	if len(body) == 0 {
		h.writeValidationError(w, []string{"request body is required"})
		return nil, false
	}

	return body, true
}

// decodeJSON unmarshals body into dst. Returns false and writes 400 on error.
func (h *Handler) decodeJSON(w http.ResponseWriter, body []byte, dst any) bool {
	if err := json.Unmarshal(body, dst); err != nil {
		h.writeValidationError(w, []string{fmt.Sprintf("invalid JSON: %s", err.Error())})
		return false
	}
	return true
}

// checkErrors writes validation errors if any. Returns false if errors were written.
func (h *Handler) checkErrors(w http.ResponseWriter, errs []string) bool {
	if len(errs) > 0 {
		h.writeValidationError(w, errs)
		return false
	}
	return true
}

// validateGetAllRequest validates the common fields for get_all requests.
// Returns a list of validation errors (empty if valid).
func validateGetAllRequest(userID, memoryType *string) []string {
	var errs []string
	if userID == nil || *userID == "" {
		errs = append(errs, "user_id is required")
	}
	if memoryType == nil || *memoryType == "" {
		errs = append(errs, "memory_type is required")
	} else {
		if _, ok := memoryTypeToDBType[*memoryType]; !ok {
			errs = append(errs, "memory_type must be one of: text_mem, act_mem, param_mem, para_mem, skill_mem, user_mem, pref_mem")
		}
	}
	return errs
}

// proxyWithBody resets r.Body from the buffered bytes and proxies to Python.
func (h *Handler) proxyWithBody(w http.ResponseWriter, r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	h.python.ProxyRequest(r.Context(), w, r)
}

// writeValidationError writes a 400 response matching the MemDB API error format.
func (h *Handler) writeValidationError(w http.ResponseWriter, errors []string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)

	msg := strings.Join(errors, "; ")
	resp := map[string]any{
		"code":    400,
		"message": "validation error: " + msg,
		"data":    nil,
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(resp)
}
