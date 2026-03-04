package text

import (
	"math"
	"sort"
	"strings"
)

// Filter ranks and selects the most relevant chunks for a query.
type Filter interface {
	Filter(chunks []string, query string) []string
}

// BM25Filter implements the BM25 ranking function for chunk retrieval.
// Default parameters follow the Okapi BM25 standard: k1=1.5, b=0.75.
type BM25Filter struct {
	topK int
	k1   float64
	b    float64
}

// NewBM25Filter creates a BM25Filter that returns at most topK chunks.
func NewBM25Filter(topK int) *BM25Filter {
	return &BM25Filter{topK: topK, k1: 1.5, b: 0.75}
}

// Filter scores each chunk against query using BM25 and returns the top-K
// highest-scoring chunks in descending score order.
//
// Edge cases:
//   - nil chunks → nil
//   - empty query → all chunks (up to topK), in original order
func (f *BM25Filter) Filter(chunks []string, query string) []string {
	if len(chunks) == 0 {
		return nil
	}

	queryTerms := tokenize(query)
	if len(queryTerms) == 0 {
		// empty query: return all chunks up to topK in original order
		if len(chunks) <= f.topK {
			return chunks
		}
		return chunks[:f.topK]
	}

	// tokenize every document
	docs := make([][]string, len(chunks))
	totalLen := 0
	for i, ch := range chunks {
		docs[i] = tokenize(ch)
		totalLen += len(docs[i])
	}
	n := len(chunks)
	avgDL := float64(totalLen) / float64(n)

	// document frequency per query term
	df := make(map[string]int, len(queryTerms))
	for _, term := range queryTerms {
		for _, doc := range docs {
			if termFreq(doc, term) > 0 {
				df[term]++
			}
		}
	}

	type scored struct {
		idx   int
		score float64
	}
	results := make([]scored, n)
	for i, doc := range docs {
		dl := float64(len(doc))
		var score float64
		for _, term := range queryTerms {
			tf := float64(termFreq(doc, term))
			d := float64(df[term])
			idf := math.Log(1 + (float64(n)-d+0.5)/(d+0.5))
			score += idf * (tf * (f.k1 + 1)) / (tf + f.k1*(1-f.b+f.b*dl/avgDL))
		}
		results[i] = scored{idx: i, score: score}
	}

	sort.Slice(results, func(a, b int) bool {
		return results[a].score > results[b].score
	})

	k := f.topK
	if k > n {
		k = n
	}
	out := make([]string, 0, k)
	for _, r := range results[:k] {
		out = append(out, chunks[r.idx])
	}
	return out
}

// tokenize lowercases s and splits on whitespace.
func tokenize(s string) []string {
	return strings.Fields(strings.ToLower(s))
}

// termFreq counts occurrences of term in tokens.
func termFreq(tokens []string, term string) int {
	count := 0
	for _, t := range tokens {
		if t == term {
			count++
		}
	}
	return count
}
