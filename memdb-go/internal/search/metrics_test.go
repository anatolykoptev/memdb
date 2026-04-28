package search

import "testing"

// TestBucketTopK pins the bucketing function so the F9 RecallBudget metric's
// `top_k` label always lands in {1,3,5,10,20,30,50,100}.  Out-of-range and
// overflow inputs are covered.
func TestBucketTopK(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int
		want int
	}{
		{0, 1},
		{-5, 1},
		{1, 1},
		{2, 3},
		{3, 3},
		{4, 5},
		{5, 5},
		{6, 10},
		{10, 10},
		{15, 20},
		{20, 20},
		{25, 30},
		{30, 30},
		{31, 50},
		{50, 50},
		{75, 100},
		{100, 100},
		{1_000_000, 100}, // overflow → clamped to top bucket
	}
	for _, c := range cases {
		got := bucketTopK(c.in)
		if got != c.want {
			t.Errorf("bucketTopK(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestBucketTopKBucketsSorted ensures the bucket slice is ASC — bucketTopK
// relies on this for its first-fit walk.
func TestBucketTopKBucketsSorted(t *testing.T) {
	t.Parallel()
	for i := 1; i < len(recallBudgetTopKBuckets); i++ {
		if recallBudgetTopKBuckets[i] <= recallBudgetTopKBuckets[i-1] {
			t.Errorf("recallBudgetTopKBuckets not strictly ASC at index %d: %v",
				i, recallBudgetTopKBuckets)
		}
	}
}
