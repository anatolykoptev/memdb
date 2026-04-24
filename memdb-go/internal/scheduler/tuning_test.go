// Package scheduler — tuning_test.go: M4 D3 env-readable hyperparameter tests.
package scheduler

import (
	"math"
	"testing"
)

func TestTuning_EpisodicMinClusterSize(t *testing.T) {
	if got := episodicMinClusterSize(); got != defaultEpisodicMinClusterSize {
		t.Fatalf("default: got %d want %d", got, defaultEpisodicMinClusterSize)
	}
	t.Setenv("MEMDB_D3_MIN_CLUSTER_RAW", "5")
	if got := episodicMinClusterSize(); got != 5 {
		t.Fatalf("override: got %d want 5", got)
	}
	for _, v := range []string{"0", "1", "51", "abc"} {
		t.Setenv("MEMDB_D3_MIN_CLUSTER_RAW", v)
		if got := episodicMinClusterSize(); got != defaultEpisodicMinClusterSize {
			t.Errorf("invalid %q: got %d want %d", v, got, defaultEpisodicMinClusterSize)
		}
	}
}

func TestTuning_SemanticMinClusterSize(t *testing.T) {
	if got := semanticMinClusterSize(); got != defaultSemanticMinClusterSize {
		t.Fatalf("default: got %d want %d", got, defaultSemanticMinClusterSize)
	}
	t.Setenv("MEMDB_D3_MIN_CLUSTER_EPISODIC", "4")
	if got := semanticMinClusterSize(); got != 4 {
		t.Fatalf("override: got %d want 4", got)
	}
	for _, v := range []string{"1", "51", "bad"} {
		t.Setenv("MEMDB_D3_MIN_CLUSTER_EPISODIC", v)
		if got := semanticMinClusterSize(); got != defaultSemanticMinClusterSize {
			t.Errorf("invalid %q: got %d want %d", v, got, defaultSemanticMinClusterSize)
		}
	}
}

func TestTuning_EpisodicCosineThreshold(t *testing.T) {
	if got := episodicCosineThreshold(); math.Abs(got-defaultEpisodicCosineThreshold) > 1e-9 {
		t.Fatalf("default: got %v want %v", got, defaultEpisodicCosineThreshold)
	}
	t.Setenv("MEMDB_D3_COS_THRESHOLD_RAW", "0.8")
	if got := episodicCosineThreshold(); math.Abs(got-0.8) > 1e-9 {
		t.Fatalf("override: got %v want 0.8", got)
	}
	for _, v := range []string{"-0.1", "1.1", "bad"} {
		t.Setenv("MEMDB_D3_COS_THRESHOLD_RAW", v)
		if got := episodicCosineThreshold(); math.Abs(got-defaultEpisodicCosineThreshold) > 1e-9 {
			t.Errorf("invalid %q: got %v want %v", v, got, defaultEpisodicCosineThreshold)
		}
	}
}

func TestTuning_SemanticCosineThreshold(t *testing.T) {
	if got := semanticCosineThreshold(); math.Abs(got-defaultSemanticCosineThreshold) > 1e-9 {
		t.Fatalf("default: got %v want %v", got, defaultSemanticCosineThreshold)
	}
	t.Setenv("MEMDB_D3_COS_THRESHOLD_EPISODIC", "0.55")
	if got := semanticCosineThreshold(); math.Abs(got-0.55) > 1e-9 {
		t.Fatalf("override: got %v want 0.55", got)
	}
}
