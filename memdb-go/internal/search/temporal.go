// Package search — temporal scope detection for search queries.
package search

import (
	"regexp"
	"time"
)

// temporalPattern maps a compiled regex to a time duration.
type temporalPattern struct {
	re   *regexp.Regexp
	days int
}

// temporalPatterns — EN + RU patterns, checked in order (first match wins).
// Matches Python's task_goal_parser.py _TEMPORAL_PATTERNS.
//
// NOTE: Go's regexp2-less RE2 engine does not support \b for non-ASCII.
// We use (?:^|\s) and (?:\s|$) as word boundaries for Cyrillic patterns,
// and keep \b only for ASCII English patterns.
var temporalPatterns = []temporalPattern{
	// last 24 hours — English
	{regexp.MustCompile(`(?i)\b(?:today|yesterday|last\s*(?:24\s*h|night))\b`), 1},
	// last 24 hours — Russian
	{regexp.MustCompile(`(?i)(?:^|\s)(?:сегодня|вчера|за\s*(?:последние\s*)?сутки)(?:\s|$)`), 1},
	// last 7 days — English
	{regexp.MustCompile(`(?i)\b(?:(?:this|last|past)\s*week|last\s*(?:7|seven)\s*days)\b`), 7},
	// last 7 days — Russian
	{regexp.MustCompile(`(?i)(?:^|\s)(?:(?:на\s*)?(?:этой|прошлой)\s*неделе|за\s*(?:последн(?:юю|ие)\s*)?неделю|последни[ех]\s*7\s*дн)`), 7},
	// last 30 days — English
	{regexp.MustCompile(`(?i)\b(?:(?:this|last|past)\s*month|last\s*(?:30|thirty)\s*days)\b`), 30},
	// last 30 days — Russian
	{regexp.MustCompile(`(?i)(?:^|\s)(?:(?:в\s*)?(?:этом|прошлом)\s*месяце|за\s*(?:последний\s*)?месяц|последни[ех]\s*30\s*дн)`), 30},
	// last 90 days / quarter — English
	{regexp.MustCompile(`(?i)\b(?:(?:this|last|past)\s*(?:quarter|3\s*months)|last\s*(?:90|ninety)\s*days)\b`), 90},
	// last 90 days / quarter — Russian
	{regexp.MustCompile(`(?i)(?:^|\s)(?:за\s*(?:последни[ехй]\s*)?(?:3\s*месяц|квартал))`), 90},
	// generic recent — English
	{regexp.MustCompile(`(?i)\b(?:recently|recent)\b`), 30},
	// generic recent — Russian
	{regexp.MustCompile(`(?i)(?:^|\s)(?:недавно|последн(?:ее|ие|ий))(?:\s|$)`), 30},
}

// DetectTemporalCutoff scans the query for temporal intent and returns
// the UTC cutoff time. Returns zero time if no temporal pattern is detected.
func DetectTemporalCutoff(query string) time.Time {
	for _, p := range temporalPatterns {
		if p.re.MatchString(query) {
			return time.Now().UTC().AddDate(0, 0, -p.days)
		}
	}
	return time.Time{}
}
