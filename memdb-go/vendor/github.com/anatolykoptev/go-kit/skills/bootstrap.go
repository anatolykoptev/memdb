package skills

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// File and directory permissions for InitWorkspace. Workspaces hold
// agent identity / config / secrets-adjacent files; restrict to owner.
const (
	dirPerm  os.FileMode = 0o750
	filePerm os.FileMode = 0o600
)

// InitWorkspace creates dir (mode 0750) and writes the entries in
// defaults if missing. Existing files are not overwritten. Nested keys
// are supported — parent dirs are created at mode 0750. File mode is
// 0600. Errors are logged via slog and not returned — best-effort UX.
func InitWorkspace(dir string, defaults map[string][]byte) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		slog.Warn("skills.InitWorkspace: failed to create workspace dir",
			slog.String("dir", dir),
			slog.String("err", err.Error()))
		return
	}

	for key, value := range defaults {
		path := filepath.Join(dir, key)
		parent := filepath.Dir(path)

		if parent != dir {
			if err := os.MkdirAll(parent, dirPerm); err != nil {
				slog.Warn("skills.InitWorkspace: failed to create parent dir",
					slog.String("parent", parent),
					slog.String("err", err.Error()))
				continue
			}
		}

		created, err := createDefaultFile(path, value)
		if err != nil {
			slog.Warn("skills.InitWorkspace: failed to write default file",
				slog.String("path", path),
				slog.String("err", err.Error()))
			continue
		}
		if !created {
			slog.Debug("skills.InitWorkspace: skipping existing file",
				slog.String("path", path))
			continue
		}

		slog.Debug("skills.InitWorkspace: wrote default file",
			slog.String("path", path))
	}
}

// createDefaultFile writes value to path only if nothing exists there. The
// file is opened with O_CREATE|O_EXCL, so the existence check and the creation
// are one atomic step: a file (or symlink, even a dangling one) that appears
// concurrently is never truncated or written through. created=false with a nil
// error means the entry already existed and was left untouched.
func createDefaultFile(path string, value []byte) (created bool, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm) //nolint:gosec // G304: path is workspace dir + operator-supplied key
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	_, werr := f.Write(value)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		// We created the file a moment ago; do not leave a truncated default
		// behind that the next run would treat as the user's own.
		_ = os.Remove(path)
		return false, err
	}
	return true, nil
}
