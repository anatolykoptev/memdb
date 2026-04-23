package handlers

import "strings"

// classifyFlushError extracts a reason label from a wrapped buffer-flush error.
// Matches the fmt.Errorf prefix strings used inside flushBuffer / runFinePipeline.
func classifyFlushError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "buffer flush: lua script"):
		return "lua"
	case strings.Contains(s, "buffer flush: extract and dedup"):
		return "parse"
	case strings.Contains(s, "buffer flush: insert nodes"):
		return "db"
	default:
		return "other"
	}
}
