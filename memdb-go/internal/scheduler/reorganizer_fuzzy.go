package scheduler

// reorganizer_fuzzy.go — fuzzy UUID resolution for LLM-returned IDs.
//
// Prod logs show the LLM occasionally returns a UUID with a single extra, missing,
// or substituted character. resolveFuzzyID recovers these cases using Levenshtein
// distance ≤ fuzzyIDMaxDistance, requiring an unambiguous single match.

import (
	"log/slog"
	"strings"
)

// resolveClusterID returns the cluster-canonical form of id, logging the outcome.
// Tries exact match first, then fuzzy match. Returns ("", false) on no match.
func (r *Reorganizer) resolveClusterID(id string, clusterSet map[string]bool, ids []string, field string) (string, bool) {
	if clusterSet[id] {
		return id, true
	}
	if resolved, ok := resolveFuzzyID(id, ids); ok {
		r.logger.Info("reorganizer: fuzzy-matched LLM id",
			slog.String("field", field),
			slog.String("original", id),
			slog.String("resolved", resolved))
		return resolved, true
	}
	r.logger.Warn("reorganizer: LLM returned id not in cluster, skipping",
		slog.String("field", field),
		slog.String("id", id))
	return "", false
}

// resolveFuzzyID finds a unique cluster ID within Levenshtein distance fuzzyIDMaxDistance
// of candidate (case-insensitive). Returns ("", false) if 0 or ≥2 IDs match.
func resolveFuzzyID(candidate string, ids []string) (string, bool) {
	if candidate == "" || len(ids) == 0 {
		return "", false
	}
	lower := strings.ToLower(candidate)
	var match string
	count := 0
	for _, id := range ids {
		d := levenshtein(lower, strings.ToLower(id))
		if d <= fuzzyIDMaxDistance {
			match = id
			count++
			if count > 1 {
				return "", false // ambiguous
			}
		}
	}
	if count == 1 {
		return match, true
	}
	return "", false
}

// levenshtein computes the edit distance between two strings using the classic
// two-row iterative DP algorithm (O(m*n) time, O(min(m,n)) space).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	curr := make([]int, len(rb)+1)
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
