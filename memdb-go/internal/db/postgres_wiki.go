// postgres_wiki.go — wiki_pages table operations (Karpathy LLM Wiki layer).
//
// Schema: see migrations/0029_wiki_pages.sql.
//
// Contract: every row is keyed by (cube_id, slug) under a unique index.
// Upsert is the canonical write — callers should not InsertWikiPage
// followed by UpdateWikiPage when they want "create or replace"
// semantics. Soft-delete is intentionally absent (the wiki is a
// compounding artifact); hard delete is permitted for cube tear-down.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// WikiPage mirrors a row from memos_graph.wiki_pages. Embedding is
// returned as []float32 for symmetry with VectorSearchResult; nil
// when the row was inserted without one (rare — only allowed for
// stub pages awaiting an embed pass).
type WikiPage struct {
	ID            string
	CubeID        string
	Slug          string
	Title         string
	Body          string
	ParentSources []string
	Embedding     []float32
	Version       int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// UpsertWikiPageParams is the input for UpsertWikiPage. Embedding is
// optional — pass nil when the caller hasn't computed one yet (the
// row will still land, just without retrieve-time discoverability).
type UpsertWikiPageParams struct {
	CubeID        string
	Slug          string
	Title         string
	Body          string
	ParentSources []string
	Embedding     []float32
}

// ErrWikiPageNotFound is returned by GetWikiPage / GetWikiPageByID
// when no row matches. Callers should not log-and-return on this —
// it's a normal cache-miss for the "page does not exist yet" path.
var ErrWikiPageNotFound = errors.New("wiki page not found")

const wikiPageColumns = `
    id, cube_id, slug, title, body, parent_sources,
    version, created_at, updated_at`

// UpsertWikiPage inserts a new wiki page or updates the existing row
// for (cube_id, slug). On update: title / body / parent_sources /
// embedding are replaced and version is incremented atomically.
//
// Returns the resulting row (with the post-write version + timestamps)
// regardless of whether the call was an insert or an update.
func (p *Postgres) UpsertWikiPage(ctx context.Context, params UpsertWikiPageParams) (WikiPage, error) {
	if err := validateWikiKey(params.CubeID, params.Slug); err != nil {
		return WikiPage{}, fmt.Errorf("UpsertWikiPage: %w", err)
	}
	parents := params.ParentSources
	if parents == nil {
		parents = []string{}
	}
	q := `
INSERT INTO memos_graph.wiki_pages
    (cube_id, slug, title, body, parent_sources, embedding)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (cube_id, slug) DO UPDATE
SET title          = EXCLUDED.title,
    body           = EXCLUDED.body,
    parent_sources = EXCLUDED.parent_sources,
    embedding      = COALESCE(EXCLUDED.embedding, memos_graph.wiki_pages.embedding),
    version        = memos_graph.wiki_pages.version + 1
RETURNING ` + wikiPageColumns
	row := p.pool.QueryRow(ctx, q,
		params.CubeID,
		params.Slug,
		params.Title,
		params.Body,
		parents,
		formatOptionalVector(params.Embedding),
	)
	return scanWikiPageRow(row)
}

// GetWikiPage fetches one page by (cube_id, slug). Returns
// ErrWikiPageNotFound when no row matches.
func (p *Postgres) GetWikiPage(ctx context.Context, cubeID, slug string) (WikiPage, error) {
	if err := validateWikiKey(cubeID, slug); err != nil {
		return WikiPage{}, fmt.Errorf("GetWikiPage: %w", err)
	}
	row := p.pool.QueryRow(ctx, `
SELECT `+wikiPageColumns+`
FROM memos_graph.wiki_pages
WHERE cube_id = $1 AND slug = $2`, cubeID, slug)
	page, err := scanWikiPageRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return WikiPage{}, ErrWikiPageNotFound
	}
	return page, err
}

// ListWikiPages returns the catalog for a cube ordered by most-recent
// update first. Used by the /product/wiki/index endpoint to render
// index.md. The body field is left empty in the result to keep the
// payload small — callers that want body content should follow up
// with GetWikiPage per slug.
func (p *Postgres) ListWikiPages(ctx context.Context, cubeID string, limit int) ([]WikiPage, error) {
	if cubeID == "" {
		return nil, errors.New("ListWikiPages: empty cube_id")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := p.pool.Query(ctx, `
SELECT id, cube_id, slug, title, '' AS body, parent_sources, version, created_at, updated_at
FROM memos_graph.wiki_pages
WHERE cube_id = $1
ORDER BY updated_at DESC
LIMIT $2`, cubeID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListWikiPages: %w", err)
	}
	defer rows.Close()

	var out []WikiPage
	for rows.Next() {
		page, err := scanWikiPageRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, page)
	}
	return out, rows.Err()
}

// WikiSearchHit pairs a wiki page with its cosine similarity to the search
// query. Score is in [0, 1] where 1 is identical and 0 is orthogonal —
// matches the relativity scale on retrieved memories so the chat retrieval
// slot can mix them into the same ranked list without rescaling.
type WikiSearchHit struct {
	Page  WikiPage
	Score float64
}

// SearchWikiByCosine returns the top-K wiki pages most similar to the supplied
// query embedding for a cube, ordered by pgvector halfvec_cosine distance
// ascending (closest first). Pages without an embedding are skipped. Body is
// returned alongside metadata so callers (chat prompt injector / retrieval
// slot) can either paste the markdown into the system prompt or merge into
// the memory list.
//
// Empty embedding or limit ≤ 0 → returns nil, nil. Limit clamped to 50 to
// bound prompt growth.
//
// Score = 1 - distance: pgvector's halfvec_cosine returns distance in [0, 2]
// for normalised embeddings, but in practice on our 1024-dim e5 vectors the
// range is [0, 1] (orthogonal pages cluster around 0.95+). We expose
// similarity in [0, 1] so callers can compare against the same threshold
// used for memory relativity.
func (p *Postgres) SearchWikiByCosine(ctx context.Context, cubeID string, query []float32, limit int) ([]WikiSearchHit, error) {
	if cubeID == "" {
		return nil, errors.New("SearchWikiByCosine: empty cube_id")
	}
	if len(query) == 0 || limit <= 0 {
		return nil, nil
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := p.pool.Query(ctx, `
SELECT `+wikiPageColumns+`,
    1 - (embedding::halfvec(1024) <=> $2::halfvec(1024)) AS score
FROM memos_graph.wiki_pages
WHERE cube_id = $1
  AND embedding IS NOT NULL
ORDER BY embedding::halfvec(1024) <=> $2::halfvec(1024)
LIMIT $3`, cubeID, FormatVector(query), limit)
	if err != nil {
		return nil, fmt.Errorf("SearchWikiByCosine: %w", err)
	}
	defer rows.Close()
	var out []WikiSearchHit
	for rows.Next() {
		var hit WikiSearchHit
		if err := rows.Scan(
			&hit.Page.ID,
			&hit.Page.CubeID,
			&hit.Page.Slug,
			&hit.Page.Title,
			&hit.Page.Body,
			&hit.Page.ParentSources,
			&hit.Page.Version,
			&hit.Page.CreatedAt,
			&hit.Page.UpdatedAt,
			&hit.Score,
		); err != nil {
			return nil, fmt.Errorf("scan wiki search hit: %w", err)
		}
		out = append(out, hit)
	}
	return out, rows.Err()
}

// DeleteWikiPage removes one page by (cube_id, slug). Returns
// ErrWikiPageNotFound when no row matched. Used only for cube
// tear-down or explicit operator action — wiki maintenance should
// upsert with new content rather than delete.
func (p *Postgres) DeleteWikiPage(ctx context.Context, cubeID, slug string) error {
	if err := validateWikiKey(cubeID, slug); err != nil {
		return fmt.Errorf("DeleteWikiPage: %w", err)
	}
	tag, err := p.pool.Exec(ctx, `
DELETE FROM memos_graph.wiki_pages
WHERE cube_id = $1 AND slug = $2`, cubeID, slug)
	if err != nil {
		return fmt.Errorf("DeleteWikiPage: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrWikiPageNotFound
	}
	return nil
}

// validateWikiKey enforces non-empty cube_id and slug at the DB
// boundary. Slug shape (no '..' / no leading slash / no backslash) is
// enforced by the wiki package's validateSlug at the API boundary —
// duplicating it here would invite drift.
func validateWikiKey(cubeID, slug string) error {
	if cubeID == "" {
		return errors.New("empty cube_id")
	}
	if slug == "" {
		return errors.New("empty slug")
	}
	return nil
}

// formatOptionalVector returns nil for an empty/nil embedding so
// pgx writes SQL NULL (preserving COALESCE semantics on update),
// otherwise the pgvector text format the column expects.
func formatOptionalVector(v []float32) any {
	if len(v) == 0 {
		return nil
	}
	return FormatVector(v)
}

// rowScanner narrows pgx.Row and pgx.Rows to the single Scan method
// scanWikiPageRow needs. Lets the same scanner serve both single-row
// and multi-row paths without duplicating the column list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanWikiPageRow(r rowScanner) (WikiPage, error) {
	var w WikiPage
	if err := r.Scan(
		&w.ID,
		&w.CubeID,
		&w.Slug,
		&w.Title,
		&w.Body,
		&w.ParentSources,
		&w.Version,
		&w.CreatedAt,
		&w.UpdatedAt,
	); err != nil {
		return WikiPage{}, fmt.Errorf("scan wiki_pages: %w", err)
	}
	return w, nil
}
