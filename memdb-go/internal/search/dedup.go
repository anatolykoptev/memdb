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
if len(items) <= 1 {
if len(items) == 1 {
if items[0].MemType == "preference" {
return nil, items
}
return items, nil
}
return nil, nil
}

// Precompute query cosine similarity for all items.
// This is the relevance term in the MMR formula: sim(item, query).
// Normalized to [0,1] to match the diversity term from simMatrix.
querySimCache := make([]float64, len(items))
for i, item := range items {
if len(item.Embedding) > 0 && len(queryVec) > 0 {
raw := CosineSimilarity(queryVec, item.Embedding)
querySimCache[i] = (float64(raw) + 1.0) / 2.0
} else {
querySimCache[i] = item.Score // fallback: no embedding available
}
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

// Order all items by query cosine similarity descending (same metric as Phase 2).
// Previously used item.Score (mixed vector+fulltext); querySimCache is pure cosine(item,query).
orderedByRelevance := make([]int, len(items))
for i := range orderedByRelevance {
orderedByRelevance[i] = i
}
sort.SliceStable(orderedByRelevance, func(i, j int) bool {
return querySimCache[orderedByRelevance[i]] > querySimCache[orderedByRelevance[j]]
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
lambdaRelevance := lambda
const similarityThreshold float32 = 0.9
alphaExponential := DefaultMMRAlpha

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

// Real MMR: relevance = cosine(item, query) — same metric as diversity.
relevance := querySimCache[idx]

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
