package scheduler

import (
	"testing"
)

func makeNodes(n int) []memNode {
	nodes := make([]memNode, n)
	for i := range nodes {
		nodes[i] = memNode{ID: string(rune('a' + i)), Text: "text"}
	}
	return nodes
}

func TestSplitLargeCluster(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		clusterSize int
		maxSize     int
		wantChunks  int
		wantLast    int
	}{
		{"empty", 0, 8, 1, 0},
		{"one node", 1, 8, 1, 1},
		{"exactly maxSize", 8, 8, 1, 8},
		{"20 nodes chunks of 8", 20, 8, 3, 4},
		{"16 nodes chunks of 8", 16, 8, 2, 8},
		{"maxSize zero passthrough", 5, 0, 1, 5},
		// N=6 cases (production maxClusterSize)
		{"exactly 6", 6, 6, 1, 6},
		{"20 nodes chunks of 6", 20, 6, 4, 2},
		{"12 nodes chunks of 6", 12, 6, 2, 6},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cluster := makeNodes(tc.clusterSize)
			got := splitLargeCluster(cluster, tc.maxSize)
			if len(got) != tc.wantChunks {
				t.Fatalf("chunks: got %d, want %d", len(got), tc.wantChunks)
			}
			if len(got[len(got)-1]) != tc.wantLast {
				t.Fatalf("last chunk size: got %d, want %d", len(got[len(got)-1]), tc.wantLast)
			}
			// verify total node count is preserved
			total := 0
			for _, sub := range got {
				total += len(sub)
			}
			if total != tc.clusterSize {
				t.Fatalf("total nodes: got %d, want %d", total, tc.clusterSize)
			}
		})
	}
}
