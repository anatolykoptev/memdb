// Package loglevel provides a shared slog.Level parser used by both
// cmd/server and cmd/mcp-server to avoid copy-paste drift.
package loglevel

import "log/slog"

// Parse converts a lowercase level string ("debug", "info", "warn",
// "error") into the corresponding slog.Level. Unknown values default
// to slog.LevelInfo.
func Parse(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
