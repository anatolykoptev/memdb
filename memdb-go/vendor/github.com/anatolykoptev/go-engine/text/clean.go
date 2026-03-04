// Package text provides string utilities for content processing.
//
// Includes HTML cleaning, whitespace normalization, smart truncation,
// and query type classification.
package text

import (
	"regexp"
	"strings"
)

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// CleanHTML strips HTML tags and trims whitespace.
func CleanHTML(s string) string {
	return strings.TrimSpace(htmlTagRe.ReplaceAllString(s, ""))
}

// CleanLines removes empty lines and trims whitespace from each line.
func CleanLines(s string) string {
	lines := strings.Split(s, "\n")
	clean := lines[:0]
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			clean = append(clean, l)
		}
	}
	return strings.Join(clean, "\n")
}

// Truncate returns the first n bytes of s.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TruncateRunes caps s at limit runes, appending suffix if truncated.
// Pass suffix="" for no suffix. Safe for UTF-8 (Cyrillic, CJK, emoji).
func TruncateRunes(s string, limit int, suffix string) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + suffix
}

// TruncateAtWord truncates a string to maxLen runes at a word boundary.
func TruncateAtWord(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	truncated := string(runes[:maxLen])
	cut := strings.LastIndex(truncated, " ")
	if cut < len(truncated)/2 {
		return truncated + "..."
	}
	return truncated[:cut] + "..."
}
