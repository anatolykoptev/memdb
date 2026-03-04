package search

import (
	"net/url"

	"github.com/anatolykoptev/go-engine/sources"
)

// FilterByScore removes results below minScore, keeping at least minKeep.
func FilterByScore(results []sources.Result, minScore float64, minKeep int) []sources.Result {
	var out []sources.Result
	for _, r := range results {
		if r.Score >= minScore {
			out = append(out, r)
		}
	}
	if len(out) < minKeep && len(results) >= minKeep {
		return results[:minKeep]
	}
	if len(out) < minKeep {
		return results
	}
	return out
}

// DedupByDomain limits results to maxPerDomain per domain.
func DedupByDomain(results []sources.Result, maxPerDomain int) []sources.Result {
	counts := make(map[string]int)
	var out []sources.Result
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		domain := u.Hostname()
		if counts[domain] < maxPerDomain {
			out = append(out, r)
			counts[domain]++
		}
	}
	return out
}
