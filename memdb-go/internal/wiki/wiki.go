// Package wiki implements the Karpathy LLM Wiki pattern on top of MemDB:
// a per-cube, LLM-maintained markdown synthesis layer that compounds over
// time instead of being re-derived on every retrieve.
//
// Authoritative storage lives in Postgres (memos_graph.wiki_pages — see
// migration 0029_wiki_pages.sql). On every write the page body is also
// exported to the canonical filesystem layout via go-kit/uploads:
//
//	$UPLOADS_ROOT/memdb-go/wiki/<cube_id>/<slug>.md
//	$UPLOADS_ROOT/memdb-go/wiki/<cube_id>/index.md   (LLM-rendered catalog)
//	$UPLOADS_ROOT/memdb-go/wiki/<cube_id>/log.md     (append-only timeline)
//
// The filesystem mirror is best-effort: the DB write is the source of
// truth; FS export failures are logged but do not fail the request. This
// gives operators an Obsidian-/git-browseable view without coupling the
// authoritative path to disk availability.
package wiki

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/uploads"
)

const (
	// uploadsService is the canonical service name under $UPLOADS_ROOT
	// where wiki pages are mirrored. Aligns with the krolik-server
	// uploads convention documented in CLAUDE.md.
	uploadsService = "memdb-go"
	// uploadsBucketPrefix is the per-cube parent under the service
	// namespace. The bucket suffix (cube_id) is appended at write time
	// so each cube gets its own browseable directory.
	uploadsBucketPrefix = "wiki"
)

// Page is the in-memory representation of a wiki page. Mirrors the row
// shape persisted in memos_graph.wiki_pages — IDs and timestamps are
// caller-supplied (DB defaults handle the unset path).
type Page struct {
	CubeID    string
	Slug      string
	Title     string
	Body      string // markdown
	Sources   []string
	Version   int
	UpdatedAt time.Time
}

// ExportToFilesystem writes the page body to the canonical uploads
// layout. Returns the absolute path written, or an error if the bucket
// could not be created or the file write failed.
//
// Slug normalization: forward slashes are kept (so "themes/family"
// nests into a subdirectory), but '..' segments are rejected so a
// caller-supplied slug cannot escape the cube directory. Filename gets
// the .md suffix appended.
func ExportToFilesystem(p Page) (string, error) {
	if p.CubeID == "" {
		return "", errors.New("wiki: empty cube_id")
	}
	if p.Slug == "" {
		return "", errors.New("wiki: empty slug")
	}
	if err := validateSlug(p.Slug); err != nil {
		return "", err
	}
	bucket, err := uploads.Bucket(uploadsService, filepath.Join(uploadsBucketPrefix, p.CubeID))
	if err != nil {
		return "", fmt.Errorf("wiki: bucket: %w", err)
	}
	rel := p.Slug + ".md"
	abs := filepath.Join(bucket, rel)
	// Ensure parent dir exists for nested slugs (themes/family.md).
	if dir := filepath.Dir(abs); dir != bucket {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("wiki: mkdir %q: %w", dir, err)
		}
	}
	body := p.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("wiki: write %q: %w", abs, err)
	}
	return abs, nil
}

// AppendLog appends a timestamped one-liner to the per-cube log.md. The
// log file is created on first call; subsequent calls append. Designed
// to mirror the Karpathy pattern of an append-only timeline of changes.
func AppendLog(cubeID, line string) error {
	if cubeID == "" {
		return errors.New("wiki: empty cube_id")
	}
	bucket, err := uploads.Bucket(uploadsService, filepath.Join(uploadsBucketPrefix, cubeID))
	if err != nil {
		return fmt.Errorf("wiki: bucket: %w", err)
	}
	abs := filepath.Join(bucket, "log.md")
	entry := fmt.Sprintf("- %s — %s\n", time.Now().UTC().Format(time.RFC3339), line)
	f, err := os.OpenFile(abs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("wiki: open log %q: %w", abs, err)
	}
	defer f.Close()
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("wiki: write log %q: %w", abs, err)
	}
	return nil
}

// CubeRoot returns the absolute filesystem path for a cube's wiki
// directory, creating it if missing. Useful for consumers that want to
// list the cube's pages or hand the path to an external tool (Obsidian,
// rg, fd, git).
func CubeRoot(cubeID string) (string, error) {
	if cubeID == "" {
		return "", errors.New("wiki: empty cube_id")
	}
	return uploads.Bucket(uploadsService, filepath.Join(uploadsBucketPrefix, cubeID))
}

// validateSlug rejects slugs that try to escape the bucket directory.
// Allowed: letters, digits, '_', '-', '/', '.'. Rejected: any '..'
// segment, leading '/', backslashes, control characters.
func validateSlug(slug string) error {
	if slug == "" {
		return errors.New("wiki: empty slug")
	}
	if strings.HasPrefix(slug, "/") {
		return errors.New("wiki: slug must not start with /")
	}
	if strings.Contains(slug, `\`) {
		return errors.New("wiki: slug must not contain backslash")
	}
	for _, seg := range strings.Split(slug, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("wiki: invalid slug segment %q", seg)
		}
	}
	for _, r := range slug {
		if r < 0x20 {
			return errors.New("wiki: slug must not contain control characters")
		}
	}
	return nil
}
