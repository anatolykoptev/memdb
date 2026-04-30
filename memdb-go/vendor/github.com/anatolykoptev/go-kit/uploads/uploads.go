// Package uploads is the canonical filesystem layout for files services
// produce on the local box: screenshots, generated images, PDFs, audit
// reports, anything else that needs to live somewhere predictable.
//
// Convention:
//
//	$UPLOADS_ROOT/<service>/<bucket>/<filename>
//
// where:
//   - $UPLOADS_ROOT defaults to $HOME/uploads (override via env)
//   - <service> is the producing service name (e.g. "go-wowa", "go-imagine", "vaelor")
//   - <bucket> is the producer's internal grouping ("screenshots", "carousels",
//     "cards", "imagined", "pdf", "audio") — chosen by the producer, kept short
//   - <filename> is the actual file
//
// Why a single pkg: every service was rolling its own /tmp/<svc>-* path
// pattern. Hard to find files, no shared retention policy, no operator
// overview. With this convention, `cd ~/uploads && ls` shows every service's
// recent output at a glance.
//
// Usage:
//
//	dir, err := uploads.Bucket("go-wowa", "screenshots")  // creates dir if needed
//	path := filepath.Join(dir, "abc.png")
//
// Or one-shot:
//
//	path, err := uploads.Path("vaelor", "imagined", "sf-rooftop.png")
package uploads

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvRoot is the env var name that overrides the default uploads root.
const EnvRoot = "UPLOADS_ROOT"

// DefaultRootRel is the path appended to $HOME when EnvRoot is unset.
const DefaultRootRel = "uploads"

// Root returns the canonical uploads root. Reads $UPLOADS_ROOT, then falls
// back to $HOME/uploads, then to /tmp/uploads if $HOME is unavailable.
// Does NOT create the directory — callers wanting on-disk presence should
// call Service or Bucket which do MkdirAll.
func Root() string {
	if v := os.Getenv(EnvRoot); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, DefaultRootRel)
	}
	return filepath.Join(os.TempDir(), "uploads")
}

// Service returns $UPLOADS_ROOT/<service> and creates it if missing.
// Use when the service wants to manage its own internal layout below the
// canonical service-namespace.
func Service(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("uploads: empty service name")
	}
	dir := filepath.Join(Root(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("uploads: mkdir %q: %w", dir, err)
	}
	return dir, nil
}

// Bucket returns $UPLOADS_ROOT/<service>/<bucket> and creates it if missing.
// The standard call-site pattern.
func Bucket(service, bucket string) (string, error) {
	if service == "" {
		return "", fmt.Errorf("uploads: empty service name")
	}
	if bucket == "" {
		return Service(service)
	}
	dir := filepath.Join(Root(), service, bucket)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("uploads: mkdir %q: %w", dir, err)
	}
	return dir, nil
}

// Path is a shortcut for filepath.Join(Bucket(service, bucket), filename).
// Returns the absolute path; the parent directory is created if missing.
// Filename is taken as-is, no sanitization — callers responsible for safety.
func Path(service, bucket, filename string) (string, error) {
	dir, err := Bucket(service, bucket)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filename), nil
}
