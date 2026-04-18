// Package handlers — request validation for typed endpoints.
// Validates request bodies against OpenAPI-generated types before proxying to Python.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/search"
)

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

	if !h.checkErrors(w, validateSearchRequest(req)) {
		return
	}

	h.proxyWithBody(w, r, normalizeSearch(body))
}

// ValidatedFeedback handles POST /product/feedback natively via the feedback
// pipeline: builds a synthetic fullAddRequest with IsFeedback=true and
// dispatches through nativeAddForCube. Replaces the Phase 4.5 Python proxy.
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

	var history []chatMessage
	if err := json.Unmarshal(*req.History, &history); err != nil {
		h.checkErrors(w, []string{"history: " + err.Error()})
		return
	}
	history = append(history, chatMessage{Role: "user", Content: *req.FeedbackContent})

	isFeedback := true
	addReq := &fullAddRequest{
		UserID:     req.UserID,
		Messages:   history,
		IsFeedback: &isFeedback,
	}

	if !h.canHandleNativeAdd(addReq) {
		h.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    503,
			"message": "feedback: backend not ready (" + h.proxyReason(addReq) + ")",
			"data":    nil,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	items, err := h.nativeAddForCube(ctx, addReq, *req.UserID)
	if err != nil {
		h.logger.Error("feedback: native add failed", slog.Any("error", err))
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    500,
			"message": "feedback processing failed",
			"data":    nil,
		})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"code":    200,
		"message": "feedback processed",
		"data":    items,
	})
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
		h.writeValidationError(w, []string{"invalid JSON: " + err.Error()})
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

// validateSearchRequest validates all fields of a searchRequest.
// Returns a list of validation errors (empty if valid).
func validateSearchRequest(req searchRequest) []string {
	errs := validateSearchRequired(req)
	errs = append(errs, validateSearchOptionals(req)...)
	return errs
}

// validateSearchRequired checks required fields: query and user_id.
func validateSearchRequired(req searchRequest) []string {
	var errs []string
	if req.Query == nil || strings.TrimSpace(*req.Query) == "" {
		errs = append(errs, "query is required and must be non-empty")
	}
	if req.UserID == nil || *req.UserID == "" {
		errs = append(errs, "user_id is required")
	}
	return errs
}

// validateSearchOptionals checks optional numeric bounds and enum fields.
func validateSearchOptionals(req searchRequest) []string {
	var errs []string
	if req.TopK != nil && *req.TopK < 1 {
		errs = append(errs, "top_k must be >= 1")
	}
	if req.Dedup != nil {
		if err := validateDedupValue(*req.Dedup); err != "" {
			errs = append(errs, err)
		}
	}
	if req.Profile != nil {
		if _, err := search.LookupProfile(*req.Profile); err != nil {
			errs = append(errs, err.Error())
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
	return errs
}

// validateDedupValue returns an error string if the dedup value is not recognized.
func validateDedupValue(dedup string) string {
	switch dedup {
	case "no", "sim", "mmr":
		return ""
	default:
		return "dedup must be one of: no, sim, mmr"
	}
}

// validateAddRequest validates mode/async_mode/user_id fields for add requests.
// Returns a list of validation errors (empty if valid).
func validateAddRequest(userID, asyncMode, mode *string) []string {
	var errs []string
	if userID == nil || *userID == "" {
		errs = append(errs, "user_id is required")
	}
	if asyncMode != nil {
		switch *asyncMode {
		case modeAsync, "sync":
		default:
			errs = append(errs, "async_mode must be one of: async, sync")
		}
	}
	if mode != nil {
		switch *mode {
		case modeFast, modeFine, modeRaw:
		default:
			errs = append(errs, "mode must be one of: fast, fine, raw")
		}
	}
	return errs
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
	_ = enc.Encode(resp)
}
