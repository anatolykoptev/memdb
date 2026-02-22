// Package search provides deduplication algorithms for search results.
// This is a Go port of Python's SearchHandler dedup methods from search_handler.py
// and the MMR/sim dedup from searcher.py.
//
// File layout:
//   dedup.go      — SearchItem type, DedupSim, DedupMMR
//   similarity.go — CosineSimilarity, CosineSimilarityMatrix, text similarity primitives
package search

import (
	"math"
	"sort"
	"strings"
)

const (
	memTypePreference = "preference"
	memTypeText       = "text"

	// mmrInitialBucketCap is the initial capacity for per-bucket index maps in MMR state.
	// 8 buckets covers typical cube counts without reallocation in most cases.
	mmrInitialBucketCap = 8
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

// DedupSim performs similarity-based deduplication.
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

// DedupMMR performs MMR-based deduplication.
//
// Phase 1: Prefill top 2 by relevance (skip exact text dupes and text-highly-similar items).
// Phase 2: Real MMR selection — relevance = cosine(item, queryVec), diversity = max cosine
// to already-selected items. Both terms use the same embedding metric (true MMR).
// lambda: configurable (DefaultMMRLambda=0.7). Exponential penalty above 0.9 with
// DefaultMMRAlpha=5.0 (soft block; old alpha=10 caused hard block at sim=0.95).
// Items without embeddings fall back to item.Score as relevance.
// Phase 3: Re-sort selected items by original relevance score.
//
// Returns separate text and preference item lists.
func DedupMMR(items []SearchItem, textTopK, prefTopK int, queryVec []float32, lambda float64) (textItems, prefItems []SearchItem) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) == 1 {
		if items[0].MemType == memTypePreference {
			return nil, items
		}
		return items, nil
	}

	st := newMMRState(items, textTopK, prefTopK, queryVec, lambda)
	st.phase1Prefill()
	st.phase2MMR()
	return st.phase3Collect()
}

// --- MMR state and phases ---

// mmrState holds all mutable state for the MMR algorithm, avoiding long parameter lists.
type mmrState struct {
	items     []SearchItem
	textTopK  int
	prefTopK  int
	lambda    float64
	simMatrix [][]float32
	// relevance score cache: cosine(item, query), normalized to [0,1]
	querySimCache []float64

	// bucket indices: item position → bucket capacity
	textBucketIndices map[int][]int
	prefBucketIndices map[int][]int

	// selection tracking
	selectedGlobal       []int
	textSelectedByBucket map[int][]int
	prefSelectedByBucket map[int][]int
	selectedTexts        map[string]struct{}
}

// newMMRState precomputes all data structures required for the MMR phases.
func newMMRState(items []SearchItem, textTopK, prefTopK int, queryVec []float32, lambda float64) *mmrState {
	st := &mmrState{
		items:                items,
		textTopK:             textTopK,
		prefTopK:             prefTopK,
		lambda:               lambda,
		simMatrix:            CosineSimilarityMatrix(items),
		querySimCache:        make([]float64, len(items)),
		textBucketIndices:    make(map[int][]int, mmrInitialBucketCap),
		prefBucketIndices:    make(map[int][]int, mmrInitialBucketCap),
		selectedGlobal:       make([]int, 0, textTopK+prefTopK),
		textSelectedByBucket: make(map[int][]int, mmrInitialBucketCap),
		prefSelectedByBucket: make(map[int][]int, mmrInitialBucketCap),
		selectedTexts:        make(map[string]struct{}, textTopK+prefTopK),
	}

	// Precompute relevance scores.
	for i, item := range items {
		if len(item.Embedding) > 0 && len(queryVec) > 0 {
			raw := CosineSimilarity(queryVec, item.Embedding)
			st.querySimCache[i] = (float64(raw) + 1.0) / 2.0
		} else {
			st.querySimCache[i] = item.Score
		}
	}

	// Index items into type buckets.
	for i, item := range items {
		switch item.MemType {
		case memTypeText:
			st.textBucketIndices[item.BucketIdx] = append(st.textBucketIndices[item.BucketIdx], i)
		case memTypePreference:
			st.prefBucketIndices[item.BucketIdx] = append(st.prefBucketIndices[item.BucketIdx], i)
		}
	}

	return st
}

// phase1Prefill selects the top prefillTopN items by relevance, skipping exact and
// near-duplicate texts. Fills selectedGlobal, *SelectedByBucket, and selectedTexts.
func (st *mmrState) phase1Prefill() {
	prefillTopN := st.computePrefillN()

	orderedByRelevance := st.itemsOrderedByRelevance()
	for _, idx := range orderedByRelevance {
		if len(st.selectedGlobal) >= prefillTopN {
			break
		}
		item := st.items[idx]
		memText := strings.TrimSpace(item.Memory)

		if _, exists := st.selectedTexts[memText]; exists {
			continue
		}
		if isTextHighlySimilar(idx, memText, st.selectedGlobal, st.simMatrix, st.items) {
			continue
		}
		st.trySelectItem(idx, memText)
	}
}

// computePrefillN returns the target prefill count for phase 1.
func (st *mmrState) computePrefillN() int {
	prefillTopN := 2
	if st.prefTopK < prefillTopN {
		prefillTopN = st.prefTopK
	}
	if st.textTopK < prefillTopN {
		prefillTopN = st.textTopK
	}
	if len(st.prefBucketIndices) == 0 {
		prefillTopN = 2
		if st.textTopK < prefillTopN {
			prefillTopN = st.textTopK
		}
	}
	return prefillTopN
}

// itemsOrderedByRelevance returns item indices sorted by querySimCache descending.
func (st *mmrState) itemsOrderedByRelevance() []int {
	order := make([]int, len(st.items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return st.querySimCache[order[i]] > st.querySimCache[order[j]]
	})
	return order
}

// trySelectItem adds idx to the selection if its bucket has capacity.
func (st *mmrState) trySelectItem(idx int, memText string) {
	item := st.items[idx]
	switch item.MemType {
	case memTypeText:
		if len(st.textSelectedByBucket[item.BucketIdx]) < st.textTopK {
			st.selectedGlobal = append(st.selectedGlobal, idx)
			st.textSelectedByBucket[item.BucketIdx] = append(st.textSelectedByBucket[item.BucketIdx], idx)
			st.selectedTexts[memText] = struct{}{}
		}
	case memTypePreference:
		if len(st.prefSelectedByBucket[item.BucketIdx]) < st.prefTopK {
			st.selectedGlobal = append(st.selectedGlobal, idx)
			st.prefSelectedByBucket[item.BucketIdx] = append(st.prefSelectedByBucket[item.BucketIdx], idx)
			st.selectedTexts[memText] = struct{}{}
		}
	}
}

// phase2MMR runs the MMR greedy selection loop until all buckets are full.
func (st *mmrState) phase2MMR() {
	const similarityThreshold float32 = 0.9

	remaining := st.buildRemainingSet()
	for len(remaining) > 0 {
		bestIdx := st.pickBestMMR(remaining, similarityThreshold)
		if bestIdx == -1 {
			break
		}
		st.commitSelection(bestIdx)
		delete(remaining, bestIdx)

		if st.allBucketsFull() {
			break
		}
	}
}

// buildRemainingSet returns a set of item indices not yet selected.
func (st *mmrState) buildRemainingSet() map[int]struct{} {
	selectedSet := make(map[int]struct{}, len(st.selectedGlobal))
	for _, idx := range st.selectedGlobal {
		selectedSet[idx] = struct{}{}
	}
	remaining := make(map[int]struct{}, len(st.items)-len(st.selectedGlobal))
	for i := range st.items {
		if _, selected := selectedSet[i]; !selected {
			remaining[i] = struct{}{}
		}
	}
	return remaining
}

// pickBestMMR finds the candidate with the highest MMR score among remaining items.
func (st *mmrState) pickBestMMR(remaining map[int]struct{}, similarityThreshold float32) int {
	bestIdx := -1
	bestMMR := math.Inf(-1)

	for idx := range remaining {
		item := st.items[idx]
		if st.isBucketFull(item) {
			continue
		}
		memText := strings.TrimSpace(item.Memory)
		if _, exists := st.selectedTexts[memText]; exists {
			continue
		}
		if isTextHighlySimilar(idx, memText, st.selectedGlobal, st.simMatrix, st.items) {
			continue
		}

		mmrScore := st.computeMMRScore(idx, similarityThreshold)
		if mmrScore > bestMMR {
			bestMMR = mmrScore
			bestIdx = idx
		}
	}
	return bestIdx
}

// isBucketFull returns true if the item's bucket has reached its capacity.
func (st *mmrState) isBucketFull(item SearchItem) bool {
	switch item.MemType {
	case memTypeText:
		return len(st.textSelectedByBucket[item.BucketIdx]) >= st.textTopK
	case memTypePreference:
		return len(st.prefSelectedByBucket[item.BucketIdx]) >= st.prefTopK
	}
	return false
}

// computeMMRScore calculates the MMR score for a single candidate item.
func (st *mmrState) computeMMRScore(idx int, similarityThreshold float32) float64 {
	relevance := st.querySimCache[idx]

	var maxSim float32
	for _, j := range st.selectedGlobal {
		if st.simMatrix[idx][j] > maxSim {
			maxSim = st.simMatrix[idx][j]
		}
	}

	var diversity float64
	if maxSim > similarityThreshold {
		penaltyMultiplier := math.Exp(DefaultMMRAlpha * float64(maxSim-similarityThreshold))
		diversity = float64(maxSim) * penaltyMultiplier
	} else {
		diversity = float64(maxSim)
	}

	return st.lambda*relevance - (1.0-st.lambda)*diversity
}

// commitSelection adds bestIdx to all selection tracking structures.
func (st *mmrState) commitSelection(bestIdx int) {
	item := st.items[bestIdx]
	memText := strings.TrimSpace(item.Memory)
	st.selectedGlobal = append(st.selectedGlobal, bestIdx)
	st.selectedTexts[memText] = struct{}{}
	switch item.MemType {
	case memTypeText:
		st.textSelectedByBucket[item.BucketIdx] = append(st.textSelectedByBucket[item.BucketIdx], bestIdx)
	case memTypePreference:
		st.prefSelectedByBucket[item.BucketIdx] = append(st.prefSelectedByBucket[item.BucketIdx], bestIdx)
	}
}

// allBucketsFull returns true when every bucket has reached its capacity limit.
func (st *mmrState) allBucketsFull() bool {
	return st.bucketsFull(st.textBucketIndices, st.textSelectedByBucket, st.textTopK) &&
		st.bucketsFull(st.prefBucketIndices, st.prefSelectedByBucket, st.prefTopK)
}

// bucketsFull checks whether all buckets in a given index map are at capacity.
func (st *mmrState) bucketsFull(allIndices, selectedByBucket map[int][]int, topK int) bool {
	for bIdx, indices := range allIndices {
		limit := topK
		if len(indices) < limit {
			limit = len(indices)
		}
		if len(selectedByBucket[bIdx]) < limit {
			return false
		}
	}
	return true
}

// phase3Collect re-sorts each bucket by original score and flattens into output slices.
func (st *mmrState) phase3Collect() (textItems, prefItems []SearchItem) {
	sortBucketsByScore := func(selectedByBucket map[int][]int) {
		for bIdx, indices := range selectedByBucket {
			sort.SliceStable(indices, func(i, j int) bool {
				return st.items[indices[i]].Score > st.items[indices[j]].Score
			})
			selectedByBucket[bIdx] = indices
		}
	}
	sortBucketsByScore(st.textSelectedByBucket)
	sortBucketsByScore(st.prefSelectedByBucket)

	textItems = flattenBuckets(st.textSelectedByBucket, st.items)
	prefItems = flattenBuckets(st.prefSelectedByBucket, st.items)
	return textItems, prefItems
}

// flattenBuckets sorts bucket keys and collects items in deterministic order.
func flattenBuckets(selectedByBucket map[int][]int, items []SearchItem) []SearchItem {
	keys := make([]int, 0, len(selectedByBucket))
	for k := range selectedByBucket {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	out := make([]SearchItem, 0, len(items))
	for _, bIdx := range keys {
		for _, idx := range selectedByBucket[bIdx] {
			out = append(out, items[idx])
		}
	}
	return out
}
