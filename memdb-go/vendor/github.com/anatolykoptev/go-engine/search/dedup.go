package search

import (
	"math"
	"strings"
	"unicode"

	"github.com/anatolykoptev/go-engine/sources"
)

// DedupSnippets removes near-duplicate results based on BoW cosine similarity
// of their Content fields. When two results exceed the threshold, the one
// with the lower Score is removed.
func DedupSnippets(results []sources.Result, threshold float64) []sources.Result {
	if len(results) == 0 {
		return nil
	}

	vecs := make([]map[string]float64, len(results))
	for i, r := range results {
		vecs[i] = tokenize(r.Content)
	}

	removed := markDuplicates(results, vecs, threshold)

	var out []sources.Result
	for i, r := range results {
		if !removed[i] {
			out = append(out, r)
		}
	}
	return out
}

// markDuplicates returns a boolean mask: removed[i] is true when result i
// was superseded by a higher-scored near-duplicate.
func markDuplicates(results []sources.Result, vecs []map[string]float64, threshold float64) []bool {
	removed := make([]bool, len(results))
	for i := range results {
		if removed[i] {
			continue
		}
		for j := i + 1; j < len(results); j++ {
			if removed[j] {
				continue
			}
			if cosineSimilarity(vecs[i], vecs[j]) > threshold {
				if results[i].Score >= results[j].Score {
					removed[j] = true
				} else {
					removed[i] = true
					break // i is removed; no point comparing it further
				}
			}
		}
	}
	return removed
}

// tokenize converts text to a bag-of-words frequency vector.
func tokenize(s string) map[string]float64 {
	vec := make(map[string]float64)
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, w := range words {
		vec[w]++
	}
	return vec
}

// cosineSimilarity computes cosine similarity between two BoW vectors.
func cosineSimilarity(a, b map[string]float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for k, v := range a {
		normA += v * v
		if bv, ok := b[k]; ok {
			dot += v * bv
		}
	}
	for _, v := range b {
		normB += v * v
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
