package skillkit

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
		slog.Warn("skillkit.InitWorkspace: failed to create workspace dir",
			slog.String("dir", dir),
			slog.String("err", err.Error()))
		return
	}

	for key, value := range defaults {
		path := filepath.Join(dir, key)
		parent := filepath.Dir(path)

		if parent != dir {
			if err := os.MkdirAll(parent, dirPerm); err != nil {
				slog.Warn("skillkit.InitWorkspace: failed to create parent dir",
					slog.String("parent", parent),
					slog.String("err", err.Error()))
				continue
			}
		}

		_, err := os.Stat(path)
		if err == nil {
			slog.Debug("skillkit.InitWorkspace: skipping existing file",
				slog.String("path", path))
			continue
		}

		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("skillkit.InitWorkspace: unexpected stat error",
				slog.String("path", path),
				slog.String("err", err.Error()))
			continue
		}

		if err := os.WriteFile(path, value, filePerm); err != nil {
			slog.Warn("skillkit.InitWorkspace: failed to write default file",
				slog.String("path", path),
				slog.String("err", err.Error()))
			continue
		}

		slog.Debug("skillkit.InitWorkspace: wrote default file",
			slog.String("path", path))
	}
}
