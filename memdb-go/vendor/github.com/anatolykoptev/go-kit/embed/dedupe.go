package embed

import (
	"context"
	"sort"
)

// DedupeGroups clusters vector indices by pairwise cosine similarity using
// union-find transitive closure: if A~B and B~C then A, B, C land in one group
// even when A~C falls below the threshold.
//
// Complexity: O(n²) pairwise comparisons — suitable for ≤ a few hundred items.
// Each index appears in exactly one group. Singletons form their own 1-element
// group. Groups are sorted by their minimum index for deterministic output.
func DedupeGroups(vectors [][]float32, threshold float32) [][]int {
	n := len(vectors)
	if n == 0 {
		return nil
	}

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}

	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x]) // path compression
		}
		return parent[x]
	}
	union := func(x, y int) {
		px, py := find(x), find(y)
		if px != py {
			parent[px] = py
		}
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if Cosine(vectors[i], vectors[j]) >= threshold {
				union(i, j)
			}
		}
	}

	// Collect groups keyed by root.
	groups := make(map[int][]int, n)
	for i := 0; i < n; i++ {
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	// Build result slice sorted by min index in each group.
	result := make([][]int, 0, len(groups))
	for _, g := range groups {
		sort.Ints(g)
		result = append(result, g)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i][0] < result[j][0]
	})
	return result
}

// DedupeTexts embeds texts via e and then calls [DedupeGroups].
//
// If e is nil or texts is empty, returns one singleton group per input (no-op,
// nil error) so callers degrade cleanly when no embedder is configured.
// Embed errors are propagated as-is.
func DedupeTexts(ctx context.Context, e Embedder, texts []string, threshold float32) ([][]int, error) {
	n := len(texts)
	if n == 0 {
		return nil, nil
	}
	if e == nil {
		groups := make([][]int, n)
		for i := range groups {
			groups[i] = []int{i}
		}
		return groups, nil
	}
	vecs, err := e.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	return DedupeGroups(vecs, threshold), nil
}
