// Package search provides deduplication algorithms for search results.
// This is a Go port of Python's SearchHandler dedup methods from search_handler.py
// and the MMR/sim dedup from searcher.py.
package search

import (
	"math"
	"sort"
	"strings"
)

// SearchItem represents a single memory search result for deduplication.
type SearchItem struct {
	Memory     string         // memory text
	Score      float64        // relativity score
	MemType    string         // "text" or "preference"
	BucketIdx  int            // index in text_mem or pref_mem buckets
	Embedding  []float32      // embedding vector
	Properties map[string]any // full memory properties dict for response building
}

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

	// Pre-normalize all embeddings
	normalized := make([][]float32, n)
	for i := range items {
		emb := items[i].Embedding
		if len(emb) == 0 {
			normalized[i] = nil
			continue
		}
		var norm float32
		for _, v := range emb {
			norm += v * v
		}
		norm = float32(math.Sqrt(float64(norm)))
		if norm == 0 {
			norm = 1.0 // Avoid division by zero, matches Python norms[norms==0] = 1.0
		}
		vec := make([]float32, len(emb))
		for j, v := range emb {
			vec[j] = v / norm
		}
		normalized[i] = vec
	}

	// Compute similarity matrix via dot product of normalized vectors
	matrix := make([][]float32, n)
	for i := 0; i < n; i++ {
		matrix[i] = make([]float32, n)
		for j := 0; j < n; j++ {
			if i == j {
				matrix[i][j] = 1.0
				continue
			}
			if j < i {
				// Symmetric: reuse already computed value
				matrix[i][j] = matrix[j][i]
				continue
			}
			if normalized[i] == nil || normalized[j] == nil {
				matrix[i][j] = 0
				continue
			}
			var dot float32
			for k := range normalized[i] {
				dot += normalized[i][k] * normalized[j][k]
			}
			// Clamp NaN/Inf to 0
			f := float64(dot)
			if math.IsNaN(f) || math.IsInf(f, 0) {
				dot = 0
			}
			matrix[i][j] = dot
		}
	}

	return matrix
}

// DedupSim performs "sim" mode deduplication with cosine threshold 0.92.
// Sorts items by score descending, then greedily selects items that are below
// 0.92 cosine similarity with all already-selected items.
// Returns up to targetK items.
func DedupSim(items []SearchItem, targetK int) []SearchItem {
	if len(items) <= 1 {
		return items
	}

	// Sort by score descending
	sorted := make([]SearchItem, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Score > sorted[j].Score
	})

	// Compute similarity matrix
	simMatrix := CosineSimilarityMatrix(sorted)

	const threshold float32 = 0.92

	selected := make([]int, 0, targetK)
	for i := range sorted {
		if len(selected) >= targetK {
			break
		}
		unrelated := true
		for _, j := range selected {
			if simMatrix[i][j] > threshold {
				unrelated = false
				break
			}
		}
		if unrelated {
			selected = append(selected, i)
		}
	}

	result := make([]SearchItem, len(selected))
	for i, idx := range selected {
		result[i] = sorted[idx]
	}
	return result
}

// DedupMMR performs MMR-based deduplication matching Python's _mmr_dedup_text_memories exactly.
//
// Phase 1: Prefill top 2 by relevance (skip exact text dupes and text-highly-similar items).
// Phase 2: MMR selection with lambda=0.8, exponential penalty above similarity_threshold=0.9,
// alpha=10.0.
// Phase 3: Re-sort selected items by original relevance score.
//
// Returns separate text and preference item lists.
func DedupMMR(items []SearchItem, textTopK, prefTopK int) (textItems, prefItems []SearchItem) {
	if len(items) <= 1 {
		if len(items) == 1 {
			if items[0].MemType == "preference" {
				return nil, items
			}
			return items, nil
		}
		return nil, nil
	}

	// Compute similarity matrix
	simMatrix := CosineSimilarityMatrix(items)

	// Track per-bucket selections
	type bucketKey struct {
		memType   string
		bucketIdx int
	}

	// Indices of items per bucket for capacity checking
	textBucketIndices := map[int][]int{}
	prefBucketIndices := map[int][]int{}
	for i, item := range items {
		if item.MemType == "text" {
			textBucketIndices[item.BucketIdx] = append(textBucketIndices[item.BucketIdx], i)
		} else if item.MemType == "preference" {
			prefBucketIndices[item.BucketIdx] = append(prefBucketIndices[item.BucketIdx], i)
		}
	}

	selectedGlobal := make([]int, 0, textTopK+prefTopK)
	textSelectedByBucket := map[int][]int{}
	prefSelectedByBucket := map[int][]int{}
	selectedTexts := map[string]struct{}{}

	// Phase 1: Prefill top N by relevance
	prefillTopN := 2
	if prefTopK < prefillTopN {
		prefillTopN = prefTopK
	}
	if textTopK < prefillTopN {
		prefillTopN = textTopK
	}
	if len(prefBucketIndices) == 0 {
		// No preference memories, just use textTopK
		prefillTopN = 2
		if textTopK < prefillTopN {
			prefillTopN = textTopK
		}
	}

	// Order all items by relevance descending
	orderedByRelevance := make([]int, len(items))
	for i := range orderedByRelevance {
		orderedByRelevance[i] = i
	}
	sort.SliceStable(orderedByRelevance, func(i, j int) bool {
		return items[orderedByRelevance[i]].Score > items[orderedByRelevance[j]].Score
	})

	for _, idx := range orderedByRelevance {
		if len(selectedGlobal) >= prefillTopN {
			break
		}
		item := items[idx]
		memText := strings.TrimSpace(item.Memory)

		// Skip exact text duplicates
		if _, exists := selectedTexts[memText]; exists {
			continue
		}

		// Skip if text-highly-similar to already selected
		if isTextHighlySimilar(idx, memText, selectedGlobal, simMatrix, items) {
			continue
		}

		// Check bucket capacity
		if item.MemType == "text" {
			if len(textSelectedByBucket[item.BucketIdx]) < textTopK {
				selectedGlobal = append(selectedGlobal, idx)
				textSelectedByBucket[item.BucketIdx] = append(textSelectedByBucket[item.BucketIdx], idx)
				selectedTexts[memText] = struct{}{}
			}
		} else if item.MemType == "preference" {
			if len(prefSelectedByBucket[item.BucketIdx]) < prefTopK {
				selectedGlobal = append(selectedGlobal, idx)
				prefSelectedByBucket[item.BucketIdx] = append(prefSelectedByBucket[item.BucketIdx], idx)
				selectedTexts[memText] = struct{}{}
			}
		}
	}

	// Phase 2: MMR selection for remaining slots
	const lambdaRelevance = 0.8
	const similarityThreshold float32 = 0.9
	const alphaExponential = 10.0

	remaining := map[int]struct{}{}
	selectedSet := map[int]struct{}{}
	for _, idx := range selectedGlobal {
		selectedSet[idx] = struct{}{}
	}
	for i := range items {
		if _, selected := selectedSet[i]; !selected {
			remaining[i] = struct{}{}
		}
	}

	for len(remaining) > 0 {
		bestIdx := -1
		bestMMR := math.Inf(-1)

		for idx := range remaining {
			item := items[idx]

			// Check bucket capacity
			if item.MemType == "text" && len(textSelectedByBucket[item.BucketIdx]) >= textTopK {
				continue
			}
			if item.MemType == "preference" && len(prefSelectedByBucket[item.BucketIdx]) >= prefTopK {
				continue
			}

			// Skip exact text duplicates
			memText := strings.TrimSpace(item.Memory)
			if _, exists := selectedTexts[memText]; exists {
				continue
			}

			// Skip text-highly-similar
			if isTextHighlySimilar(idx, memText, selectedGlobal, simMatrix, items) {
				continue
			}

			relevance := item.Score

			// Compute max similarity to already selected items
			var maxSim float32
			for _, j := range selectedGlobal {
				if simMatrix[idx][j] > maxSim {
					maxSim = simMatrix[idx][j]
				}
			}

			// Exponential penalty for similarity above threshold
			var diversity float64
			if maxSim > similarityThreshold {
				penaltyMultiplier := math.Exp(alphaExponential * float64(maxSim-similarityThreshold))
				diversity = float64(maxSim) * penaltyMultiplier
			} else {
				diversity = float64(maxSim)
			}

			mmrScore := lambdaRelevance*relevance - (1.0-lambdaRelevance)*diversity

			if mmrScore > bestMMR {
				bestMMR = mmrScore
				bestIdx = idx
			}
		}

		if bestIdx == -1 {
			break
		}

		item := items[bestIdx]
		memText := strings.TrimSpace(item.Memory)
		selectedGlobal = append(selectedGlobal, bestIdx)
		selectedTexts[memText] = struct{}{}

		if item.MemType == "text" {
			textSelectedByBucket[item.BucketIdx] = append(textSelectedByBucket[item.BucketIdx], bestIdx)
		} else if item.MemType == "preference" {
			prefSelectedByBucket[item.BucketIdx] = append(prefSelectedByBucket[item.BucketIdx], bestIdx)
		}

		delete(remaining, bestIdx)

		// Early termination: all buckets full
		textAllFull := true
		for bIdx, indices := range textBucketIndices {
			limit := textTopK
			if len(indices) < limit {
				limit = len(indices)
			}
			if len(textSelectedByBucket[bIdx]) < limit {
				textAllFull = false
				break
			}
		}
		prefAllFull := true
		for bIdx, indices := range prefBucketIndices {
			limit := prefTopK
			if len(indices) < limit {
				limit = len(indices)
			}
			if len(prefSelectedByBucket[bIdx]) < limit {
				prefAllFull = false
				break
			}
		}
		if textAllFull && prefAllFull {
			break
		}
	}

	// Phase 3: Re-sort selected items by original relevance score, then split by type
	// Collect text items sorted by relevance per bucket
	for bIdx, indices := range textSelectedByBucket {
		sort.SliceStable(indices, func(i, j int) bool {
			return items[indices[i]].Score > items[indices[j]].Score
		})
		textSelectedByBucket[bIdx] = indices
	}
	for bIdx, indices := range prefSelectedByBucket {
		sort.SliceStable(indices, func(i, j int) bool {
			return items[indices[i]].Score > items[indices[j]].Score
		})
		prefSelectedByBucket[bIdx] = indices
	}

	// Flatten bucket selections into output slices, preserving bucket order
	textItems = make([]SearchItem, 0)
	// Collect all text bucket indices and sort them for deterministic output
	textBucketKeys := make([]int, 0, len(textSelectedByBucket))
	for k := range textSelectedByBucket {
		textBucketKeys = append(textBucketKeys, k)
	}
	sort.Ints(textBucketKeys)
	for _, bIdx := range textBucketKeys {
		for _, idx := range textSelectedByBucket[bIdx] {
			textItems = append(textItems, items[idx])
		}
	}

	prefItems = make([]SearchItem, 0)
	prefBucketKeys := make([]int, 0, len(prefSelectedByBucket))
	for k := range prefSelectedByBucket {
		prefBucketKeys = append(prefBucketKeys, k)
	}
	sort.Ints(prefBucketKeys)
	for _, bIdx := range prefBucketKeys {
		for _, idx := range prefSelectedByBucket[bIdx] {
			prefItems = append(prefItems, items[idx])
		}
	}

	return textItems, prefItems
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

	// If highest embedding similarity <= 0.9, skip text comparison
	if maxSim <= 0.9 {
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

	return combinedScore >= 0.92
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
func tfidfSimilarity(a, b string) float64 {
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
