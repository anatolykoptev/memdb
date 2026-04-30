package handlers

// wiki.go — HTTP handlers for the LLM Wiki layer.
//
// API surface:
//
//	POST /product/wiki/upsert      → create or replace a page (version++)
//	POST /product/wiki/get         → fetch one page by (cube_id, slug)
//	POST /product/wiki/index       → list all pages for a cube
//	POST /product/wiki/delete      → hard-delete one page (cube tear-down)
//
// On every successful upsert / delete the canonical filesystem mirror at
// $UPLOADS_ROOT/memdb-go/wiki/<cube_id>/ is updated via the wiki package.
// FS export is best-effort: an FS failure logs a warning but does not
// fail the request — the DB row is the source of truth.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/wiki"
)

// wikiUpsertRequest is the JSON body for POST /product/wiki/upsert.
// Embedding is optional — when omitted, the existing row's embedding
// is preserved (COALESCE in UpsertWikiPage). Callers that have an
// upstream embedder should pass the page-level vector here so retrieve
// injection works without a separate backfill pass.
type wikiUpsertRequest struct {
	CubeID        string    `json:"cube_id"`
	Slug          string    `json:"slug"`
	Title         string    `json:"title,omitempty"`
	Body          string    `json:"body"`
	ParentSources []string  `json:"parent_sources,omitempty"`
	Embedding     []float32 `json:"embedding,omitempty"`
}

// wikiPageDTO is the JSON response shape — mirrors db.WikiPage with
// snake_case fields and ISO timestamps the frontend / Python clients
// expect.
type wikiPageDTO struct {
	ID            string   `json:"id"`
	CubeID        string   `json:"cube_id"`
	Slug          string   `json:"slug"`
	Title         string   `json:"title"`
	Body          string   `json:"body,omitempty"`
	ParentSources []string `json:"parent_sources"`
	Version       int      `json:"version"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	FsPath        string   `json:"fs_path,omitempty"` // populated on upsert when FS export succeeds
}

// wikiKeyRequest is the small JSON body shared by /get and /delete.
type wikiKeyRequest struct {
	CubeID string `json:"cube_id"`
	Slug   string `json:"slug"`
}

// wikiIndexRequest is the JSON body for POST /product/wiki/index. Limit
// is clamped to [1, 1000]; default 200.
type wikiIndexRequest struct {
	CubeID string `json:"cube_id"`
	Limit  int    `json:"limit,omitempty"`
}

// wikiIndexResponse wraps the page list so future fields (totals,
// pagination cursor, lint status) can be added without breaking the
// response shape.
type wikiIndexResponse struct {
	CubeID string        `json:"cube_id"`
	Pages  []wikiPageDTO `json:"pages"`
}

// NativeWikiUpsert — POST /product/wiki/upsert
func (h *Handler) NativeWikiUpsert(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.writeValidationError(w, []string{"wiki upsert requires postgres"})
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	var req wikiUpsertRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}
	if !h.checkErrors(w, validateWikiUpsert(req)) {
		return
	}
	page, err := h.postgres.UpsertWikiPage(r.Context(), db.UpsertWikiPageParams{
		CubeID:        req.CubeID,
		Slug:          req.Slug,
		Title:         req.Title,
		Body:          req.Body,
		ParentSources: req.ParentSources,
		Embedding:     req.Embedding,
	})
	if err != nil {
		h.logger.Error("wiki upsert: db", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dto := wikiPageToDTO(page)
	if path := h.exportWikiPage(r.Context(), page); path != "" {
		dto.FsPath = path
	}
	if err := wiki.AppendLog(page.CubeID, "upsert "+page.Slug+" v"+strconv.Itoa(page.Version)); err != nil {
		// Non-fatal — the canonical write already succeeded.
		h.logger.Warn("wiki upsert: log append", slog.Any("error", err))
	}
	h.writeJSON(w, http.StatusOK, wrapData(dto, "wiki page upserted"))
}

// NativeWikiGet — POST /product/wiki/get
func (h *Handler) NativeWikiGet(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.writeValidationError(w, []string{"wiki get requires postgres"})
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	var req wikiKeyRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}
	if !h.checkErrors(w, validateWikiKey(req.CubeID, req.Slug)) {
		return
	}
	page, err := h.postgres.GetWikiPage(r.Context(), req.CubeID, req.Slug)
	if errors.Is(err, db.ErrWikiPageNotFound) {
		h.writeValidationError(w, []string{"wiki page not found"})
		return
	}
	if err != nil {
		h.logger.Error("wiki get: db", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.writeJSON(w, http.StatusOK, wrapData(wikiPageToDTO(page), "wiki page fetched"))
}

// NativeWikiIndex — POST /product/wiki/index
func (h *Handler) NativeWikiIndex(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.writeValidationError(w, []string{"wiki index requires postgres"})
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	var req wikiIndexRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}
	if req.CubeID == "" {
		h.writeValidationError(w, []string{"cube_id is required"})
		return
	}
	pages, err := h.postgres.ListWikiPages(r.Context(), req.CubeID, req.Limit)
	if err != nil {
		h.logger.Error("wiki index: db", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dtos := make([]wikiPageDTO, 0, len(pages))
	for _, p := range pages {
		dtos = append(dtos, wikiPageToDTO(p))
	}
	h.writeJSON(w, http.StatusOK, wrapData(wikiIndexResponse{
		CubeID: req.CubeID,
		Pages:  dtos,
	}, "wiki index"))
}

// NativeWikiDelete — POST /product/wiki/delete
func (h *Handler) NativeWikiDelete(w http.ResponseWriter, r *http.Request) {
	if h.postgres == nil {
		h.writeValidationError(w, []string{"wiki delete requires postgres"})
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	var req wikiKeyRequest
	if !h.decodeJSON(w, body, &req) {
		return
	}
	if !h.checkErrors(w, validateWikiKey(req.CubeID, req.Slug)) {
		return
	}
	if err := h.postgres.DeleteWikiPage(r.Context(), req.CubeID, req.Slug); err != nil {
		if errors.Is(err, db.ErrWikiPageNotFound) {
			h.writeValidationError(w, []string{"wiki page not found"})
			return
		}
		h.logger.Error("wiki delete: db", slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := wiki.AppendLog(req.CubeID, "delete "+req.Slug); err != nil {
		h.logger.Warn("wiki delete: log append", slog.Any("error", err))
	}
	h.writeJSON(w, http.StatusOK, wrapData(map[string]string{"status": "deleted"}, "wiki page deleted"))
}

// exportWikiPage writes the page body to the canonical FS layout.
// Returns the absolute path on success, "" on failure (failure logs
// a warning — DB write is authoritative). Kept tiny so the upsert
// handler reads top-down without a defer dance.
func (h *Handler) exportWikiPage(_ context.Context, page db.WikiPage) string {
	path, err := wiki.ExportToFilesystem(wiki.Page{
		CubeID:    page.CubeID,
		Slug:      page.Slug,
		Title:     page.Title,
		Body:      page.Body,
		Sources:   page.ParentSources,
		Version:   page.Version,
		UpdatedAt: page.UpdatedAt,
	})
	if err != nil {
		h.logger.Warn("wiki upsert: fs export",
			slog.String("cube_id", page.CubeID),
			slog.String("slug", page.Slug),
			slog.Any("error", err))
		return ""
	}
	return path
}

// validateWikiUpsert checks the request body. Slug-shape validation
// re-uses the wiki package helper so the FS export path and the API
// path agree on what's allowed.
func validateWikiUpsert(req wikiUpsertRequest) []string {
	var errs []string
	if req.CubeID == "" {
		errs = append(errs, "cube_id is required")
	}
	if req.Slug == "" {
		errs = append(errs, "slug is required")
	}
	if err := wiki.ValidateSlug(req.Slug); err != nil && req.Slug != "" {
		errs = append(errs, err.Error())
	}
	if len(req.Body) > 1<<20 {
		// 1 MiB cap on body — pages should compress synthesis, not
		// dump raw transcripts. Refuse early so a runaway caller
		// doesn't bloat the table.
		errs = append(errs, "body exceeds 1 MiB")
	}
	for i, src := range req.ParentSources {
		if src == "" {
			errs = append(errs, "parent_sources["+strconv.Itoa(i)+"] is empty")
		}
	}
	if len(req.Embedding) > 0 && len(req.Embedding) != 1024 {
		errs = append(errs, "embedding must be empty or 1024-dim")
	}
	return errs
}

// validateWikiKey is the shared validator for /get and /delete.
func validateWikiKey(cubeID, slug string) []string {
	var errs []string
	if cubeID == "" {
		errs = append(errs, "cube_id is required")
	}
	if slug == "" {
		errs = append(errs, "slug is required")
	}
	if err := wiki.ValidateSlug(slug); err != nil && slug != "" {
		errs = append(errs, err.Error())
	}
	return errs
}

// wikiPageToDTO converts the DB row to the JSON response shape.
func wikiPageToDTO(p db.WikiPage) wikiPageDTO {
	parents := p.ParentSources
	if parents == nil {
		parents = []string{}
	}
	return wikiPageDTO{
		ID:            p.ID,
		CubeID:        p.CubeID,
		Slug:          p.Slug,
		Title:         p.Title,
		Body:          p.Body,
		ParentSources: parents,
		Version:       p.Version,
		CreatedAt:     p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:     p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// wrapData mirrors the standard memdb-go response envelope: every
// successful native handler returns {"code":200,"message":...,"data":...}.
func wrapData(data any, message string) map[string]any {
	return map[string]any{
		"code":    200,
		"message": message,
		"data":    data,
	}
}

