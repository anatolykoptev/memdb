package search

// similarity.go — vector and text similarity primitives.
// Covers: CosineSimilarity, CosineSimilarityMatrix (vector math),
//         isTextHighlySimilar, diceSimilarity, bigramSimilarity, tfidfSimilarity.

import (
	"math"
	"strings"
)

const (
	// embedSimThreshold is the minimum cosine similarity between embeddings required
	// before performing the more expensive text-level comparison in isTextHighlySimilar.
	embedSimThreshold = 0.9

	// textCombinedSimThreshold is the weighted text similarity score above which two
	// memories are considered duplicates (Dice 40% + TF-IDF 35% + bigram 25%).
	textCombinedSimThreshold = 0.92
)

// CosineSimilarity computes cosine similarity between two float32 vectors.
// Returns 0 if either vector is zero-length or has zero norm.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

// CosineSimilarityMatrix computes an NxN cosine similarity matrix from item embeddings.
// Matches Python's cosine_similarity_matrix from retrieve_utils.py.
func CosineSimilarityMatrix(items []SearchItem) [][]float32 {
	n := len(items)
	if n == 0 {
		return nil
	}

	matrix := make([][]float32, n)
	for i := range matrix {
		matrix[i] = make([]float32, n)
		matrix[i][i] = 1.0 // self-similarity
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			sim := CosineSimilarity(items[i].Embedding, items[j].Embedding)
			matrix[i][j] = sim
			matrix[j][i] = sim
		}
	}

	return matrix
}

// isTextHighlySimilar checks if a candidate memory is text-highly-similar to any selected memory.
//
// Strategy (matches Python _is_text_highly_similar_optimized):
//  1. Find the single selected item with highest embedding similarity to the candidate.
//  2. If embedding similarity <= 0.9, return false (skip text comparison).
//  3. Compute weighted combination: 0.40*dice + 0.35*tfidf + 0.25*bigram.
//  4. Return true if combined score >= 0.92 threshold.
func isTextHighlySimilar(
	candidateIdx int,
	candidateText string,
	selectedGlobal []int,
	simMatrix [][]float32,
	items []SearchItem,
) bool {
	if len(selectedGlobal) == 0 {
		return false
	}

	// Find the already-selected memory with highest embedding similarity
	maxSimIdx := selectedGlobal[0]
	maxSim := simMatrix[candidateIdx][maxSimIdx]
	for _, j := range selectedGlobal[1:] {
		if simMatrix[candidateIdx][j] > maxSim {
			maxSim = simMatrix[candidateIdx][j]
			maxSimIdx = j
		}
	}

	// If highest embedding similarity <= embedSimThreshold, skip text comparison
	if maxSim <= embedSimThreshold {
		return false
	}

	// Get text of most similar memory
	mostSimilarText := strings.TrimSpace(items[maxSimIdx].Memory)

	// Calculate three similarity scores
	diceSim := diceSimilarity(candidateText, mostSimilarText)
	tfidfSim := tfidfSimilarity(candidateText, mostSimilarText)
	bigramSim := bigramSimilarity(candidateText, mostSimilarText)

	// Weighted combination: Dice (40%) + TF-IDF (35%) + 2-gram (25%)
	combinedScore := 0.40*diceSim + 0.35*tfidfSim + 0.25*bigramSim

	return combinedScore >= textCombinedSimThreshold
}

// diceSimilarity calculates character-level Dice coefficient.
// Dice = 2 * |A intersect B| / (|A| + |B|)
// Matches Python SearchHandler._dice_similarity exactly.
func diceSimilarity(a, b string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	chars1 := make(map[rune]struct{})
	for _, r := range a {
		chars1[r] = struct{}{}
	}

	chars2 := make(map[rune]struct{})
	for _, r := range b {
		chars2[r] = struct{}{}
	}

	// Count intersection
	intersection := 0
	for r := range chars1 {
		if _, ok := chars2[r]; ok {
			intersection++
		}
	}

	return 2.0 * float64(intersection) / float64(len(chars1)+len(chars2))
}

// bigramSimilarity calculates character-level 2-gram Jaccard similarity.
// Jaccard = |A intersect B| / |A union B|
// Matches Python SearchHandler._bigram_similarity exactly.
func bigramSimilarity(a, b string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	runesA := []rune(a)
	runesB := []rune(b)

	bigrams1 := make(map[string]struct{})
	if len(runesA) >= 2 {
		for i := 0; i < len(runesA)-1; i++ {
			bigrams1[string(runesA[i:i+2])] = struct{}{}
		}
	} else {
		bigrams1[string(runesA)] = struct{}{}
	}

	bigrams2 := make(map[string]struct{})
	if len(runesB) >= 2 {
		for i := 0; i < len(runesB)-1; i++ {
			bigrams2[string(runesB[i:i+2])] = struct{}{}
		}
	} else {
		bigrams2[string(runesB)] = struct{}{}
	}

	// Intersection count
	intersection := 0
	for bg := range bigrams1 {
		if _, ok := bigrams2[bg]; ok {
			intersection++
		}
	}

	// Union = |A| + |B| - |A intersect B|
	union := len(bigrams1) + len(bigrams2) - intersection
	if union == 0 {
		return 0.0
	}

	return float64(intersection) / float64(union)
}

// tfidfSimilarity calculates character-level TF-IDF cosine similarity.
// Matches Python SearchHandler._tfidf_similarity exactly.
//
// Uses character frequency as TF, with a simple 2-document IDF:
// IDF = 1.0 if char appears in both documents, 1.5 if only in one.
func tfidfSimilarity(a, b string) float64 { //nolint:cyclop // inherent complexity of character-level TF-IDF algorithm
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	// Character frequency (TF)
	tf1 := make(map[rune]int)
	for _, r := range a {
		tf1[r]++
	}
	tf2 := make(map[rune]int)
	for _, r := range b {
		tf2[r]++
	}

	// All unique characters (vocabulary)
	vocab := make(map[rune]struct{})
	for r := range tf1 {
		vocab[r] = struct{}{}
	}
	for r := range tf2 {
		vocab[r] = struct{}{}
	}

	// Simple IDF: 1.0 if char appears in both docs, 1.5 if only in one
	// Matches Python: idf[char] = 1.0 if char in tf1 and char in tf2 else 1.5
	idf := make(map[rune]float64, len(vocab))
	for ch := range vocab {
		_, in1 := tf1[ch]
		_, in2 := tf2[ch]
		if in1 && in2 {
			idf[ch] = 1.0
		} else {
			idf[ch] = 1.5
		}
	}

	// TF-IDF vectors and cosine similarity computation
	var dotProduct, norm1, norm2 float64
	for ch := range vocab {
		v1 := float64(tf1[ch]) * idf[ch]
		v2 := float64(tf2[ch]) * idf[ch]
		dotProduct += v1 * v2
		norm1 += v1 * v1
		norm2 += v2 * v2
	}

	norm1 = math.Sqrt(norm1)
	norm2 = math.Sqrt(norm2)

	if norm1 == 0 || norm2 == 0 {
		return 0.0
	}

	return dotProduct / (norm1 * norm2)
}
