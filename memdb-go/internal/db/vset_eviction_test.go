package db

// vset_eviction_test.go — tests for the per-cube VSET eviction policy.
//
// The tests exercise the REAL VSetEvictionPolicy.CheckAndEvict code path.
// The Redis layer (VCard/VSimFiltered/VRemBatch) is mocked via the
// vsetEvictionTarget interface because miniredis does not support Redis 8
// VSET commands. Removing the eviction guard (count > cap check) or the
// oldest-first sort makes these tests go RED.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

// mockEvictionCache implements vsetEvictionTarget for testing.
type mockEvictionCache struct {
	cardCount     int64
	cardErr       error
	simCandidates []VSetCandidate
	simErr        error
	removedIDs    []string
	remErr        error
	remCalled     bool
}

func (m *mockEvictionCache) VCard(_ context.Context, _ string) (int64, error) {
	return m.cardCount, m.cardErr
}

func (m *mockEvictionCache) VSimFiltered(_ context.Context, _ string, _ []float32, _ int, _ int64) ([]VSetCandidate, error) {
	return m.simCandidates, m.simErr
}

func (m *mockEvictionCache) VRemBatch(_ context.Context, _ string, nodeIDs []string) error {
	m.remCalled = true
	m.removedIDs = nodeIDs
	return m.remErr
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nil, nil))
}

// TestVSetEvictionPolicy_CheckAndEvict_UnderCap verifies that when VCard is
// at or below the cap, no eviction occurs and VRemBatch is never called.
func TestVSetEvictionPolicy_CheckAndEvict_UnderCap(t *testing.T) {
	mc := &mockEvictionCache{cardCount: 50}
	p := NewVSetEvictionPolicy(500, 1024, mc, newTestLogger())

	if err := p.CheckAndEvict(context.Background(), "cube-1"); err != nil {
		t.Fatalf("expected nil error under cap, got %v", err)
	}
	if mc.remCalled {
		t.Error("VRemBatch should not be called when count <= cap")
	}
}

// TestVSetEvictionPolicy_CheckAndEvict_AtCap verifies the boundary: count
// == cap is NOT an eviction trigger (only count > cap evicts).
func TestVSetEvictionPolicy_CheckAndEvict_AtCap(t *testing.T) {
	mc := &mockEvictionCache{cardCount: 500}
	p := NewVSetEvictionPolicy(500, 1024, mc, newTestLogger())

	if err := p.CheckAndEvict(context.Background(), "cube-1"); err != nil {
		t.Fatalf("expected nil error at cap, got %v", err)
	}
	if mc.remCalled {
		t.Error("VRemBatch should not be called when count == cap")
	}
}

// TestVSetEvictionPolicy_CheckAndEvict_OverCap verifies that when VCard
// exceeds the cap, the oldest entries (by ts) are evicted via VRemBatch.
// This is the correctness-critical test: the policy MUST evict oldest-first
// so stale dedup candidates don't survive and cause wrong LLM merges.
func TestVSetEvictionPolicy_CheckAndEvict_OverCap(t *testing.T) {
	// 510 elements, cap=500 → evict 10 + vsetEvictBatch(10) = 20 oldest.
	mc := &mockEvictionCache{
		cardCount: 510,
		simCandidates: []VSetCandidate{
			{ID: "node-newest", TS: 5000},
			{ID: "node-old-1", TS: 100},
			{ID: "node-mid", TS: 3000},
			{ID: "node-old-2", TS: 200},
			{ID: "node-old-3", TS: 300},
		},
	}
	p := NewVSetEvictionPolicy(500, 1024, mc, newTestLogger())

	if err := p.CheckAndEvict(context.Background(), "cube-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mc.remCalled {
		t.Fatal("VRemBatch should be called when count > cap")
	}

	// Expected eviction count = 510 - 500 + 10 = 20, but only 5 candidates
	// exist, so we evict all 5 (clamped to len(candidates)).
	if len(mc.removedIDs) != 5 {
		t.Fatalf("expected 5 evicted IDs (clamped to candidate count), got %d: %v", len(mc.removedIDs), mc.removedIDs)
	}

	// The evicted IDs must be sorted oldest-first by ts.
	// Expected order: node-old-1(100), node-old-2(200), node-old-3(300), node-mid(3000), node-newest(5000)
	expected := []string{"node-old-1", "node-old-2", "node-old-3", "node-mid", "node-newest"}
	for i, id := range expected {
		if mc.removedIDs[i] != id {
			t.Errorf("removedIDs[%d] = %q, want %q (oldest-first by ts)", i, mc.removedIDs[i], id)
		}
	}
}

// TestVSetEvictionPolicy_CheckAndEvict_OverCap_PartialEvict verifies that
// when there are more candidates than needed, only the required number are
// evicted (the oldest ones), not all of them.
func TestVSetEvictionPolicy_CheckAndEvict_OverCap_PartialEvict(t *testing.T) {
	// 505 elements, cap=500 → evict 5 + 10 = 15 oldest.
	candidates := make([]VSetCandidate, 50)
	for i := range candidates {
		candidates[i] = VSetCandidate{
			ID: "node-" + string(rune('A'+i)),
			TS: int64(i * 100), // node-0 oldest, node-49 newest
		}
	}
	mc := &mockEvictionCache{
		cardCount:     505,
		simCandidates: candidates,
	}
	p := NewVSetEvictionPolicy(500, 1024, mc, newTestLogger())

	if err := p.CheckAndEvict(context.Background(), "cube-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// evictCount = 505 - 500 + 10 = 15, candidates = 50 → evict 15 oldest.
	if len(mc.removedIDs) != 15 {
		t.Fatalf("expected 15 evicted IDs, got %d", len(mc.removedIDs))
	}
	// First evicted should be the oldest (ts=0).
	if mc.removedIDs[0] != "node-A" {
		t.Errorf("first evicted = %q, want node-A (oldest, ts=0)", mc.removedIDs[0])
	}
	// Last evicted should be the 15th oldest (ts=1400).
	if mc.removedIDs[14] != "node-"+string(rune('A'+14)) {
		t.Errorf("last evicted = %q, want node-O (15th oldest)", mc.removedIDs[14])
	}
}

// TestVSetEvictionPolicy_CheckAndEvict_Error verifies that when VCard fails,
// the error is returned, VRemBatch is not called, and the metric outcome
// would be "error" (verified by the error return path).
func TestVSetEvictionPolicy_CheckAndEvict_Error(t *testing.T) {
	cardErr := errors.New("redis: connection refused")
	mc := &mockEvictionCache{
		cardErr: cardErr,
	}
	p := NewVSetEvictionPolicy(500, 1024, mc, newTestLogger())

	err := p.CheckAndEvict(context.Background(), "cube-1")
	if err == nil {
		t.Fatal("expected error when VCard fails, got nil")
	}
	if !errors.Is(err, cardErr) {
		t.Errorf("expected error to wrap cardErr, got %v", err)
	}
	if mc.remCalled {
		t.Error("VRemBatch should not be called when VCard fails")
	}
}

// TestVSetEvictionPolicy_CheckAndEvict_VSimError verifies that when VSim
// fails, the error is returned and VRemBatch is not called.
func TestVSetEvictionPolicy_CheckAndEvict_VSimError(t *testing.T) {
	simErr := errors.New("vsim: dim mismatch")
	mc := &mockEvictionCache{
		cardCount: 510,
		simErr:    simErr,
	}
	p := NewVSetEvictionPolicy(500, 1024, mc, newTestLogger())

	err := p.CheckAndEvict(context.Background(), "cube-1")
	if err == nil {
		t.Fatal("expected error when VSim fails, got nil")
	}
	if !errors.Is(err, simErr) {
		t.Errorf("expected error to wrap simErr, got %v", err)
	}
	if mc.remCalled {
		t.Error("VRemBatch should not be called when VSim fails")
	}
}

// TestVSetEvictionPolicy_CheckAndEvict_VRemBatchError verifies that when
// VRemBatch fails, the error is returned.
func TestVSetEvictionPolicy_CheckAndEvict_VRemBatchError(t *testing.T) {
	remErr := errors.New("vrem: pipeline failed")
	mc := &mockEvictionCache{
		cardCount: 510,
		simCandidates: []VSetCandidate{
			{ID: "node-1", TS: 100},
		},
		remErr: remErr,
	}
	p := NewVSetEvictionPolicy(500, 1024, mc, newTestLogger())

	err := p.CheckAndEvict(context.Background(), "cube-1")
	if err == nil {
		t.Fatal("expected error when VRemBatch fails, got nil")
	}
	if !errors.Is(err, remErr) {
		t.Errorf("expected error to wrap remErr, got %v", err)
	}
}

// TestVSetEvictionPolicy_CheckAndEvict_EmptySimResults verifies that when
// VCard > cap but VSim returns no candidates (key dropped concurrently),
// the policy treats it as a miss rather than an error.
func TestVSetEvictionPolicy_CheckAndEvict_EmptySimResults(t *testing.T) {
	mc := &mockEvictionCache{
		cardCount:     510,
		simCandidates: nil,
	}
	p := NewVSetEvictionPolicy(500, 1024, mc, newTestLogger())

	if err := p.CheckAndEvict(context.Background(), "cube-1"); err != nil {
		t.Fatalf("expected nil error for empty VSim results, got %v", err)
	}
	if mc.remCalled {
		t.Error("VRemBatch should not be called when VSim returns empty")
	}
}

// TestVSetEvictionPolicy_NegativeCap verifies that a negative cap is clamped
// to zero, so every VAdd triggers eviction (useful for testing/disabling).
func TestVSetEvictionPolicy_NegativeCap(t *testing.T) {
	mc := &mockEvictionCache{
		cardCount: 1,
		simCandidates: []VSetCandidate{
			{ID: "node-1", TS: 100},
		},
	}
	p := NewVSetEvictionPolicy(-5, 1024, mc, newTestLogger())

	if p.cap != 0 {
		t.Errorf("negative cap should clamp to 0, got %d", p.cap)
	}
	// count=1 > cap=0 → eviction triggered.
	if err := p.CheckAndEvict(context.Background(), "cube-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mc.remCalled {
		t.Error("VRemBatch should be called when cap=0 and count=1")
	}
}

// TestVSetEvictionPolicy_DefaultEmbedDim verifies that embedDim=0 defaults to
// 1024 (multilingual-e5-large).
func TestVSetEvictionPolicy_DefaultEmbedDim(t *testing.T) {
	mc := &mockEvictionCache{cardCount: 0}
	p := NewVSetEvictionPolicy(500, 0, mc, newTestLogger())
	if p.embedDim != 1024 {
		t.Errorf("expected default embedDim=1024, got %d", p.embedDim)
	}
}

// --- extractTSFromAttr unit tests ---

func TestExtractTSFromAttr(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
	}{
		{"basic", `{"m":"hello","ts":1234567890}`, 1234567890},
		{"zero ts", `{"m":"","ts":0}`, 0},
		{"large ts", `{"m":"test","ts":1700000000}`, 1700000000},
		{"no ts field", `{"m":"test"}`, 0},
		{"empty", "", 0},
		{"ts only", `{"ts":999}`, 999},
		{"with whitespace", `{"m":"x", "ts": 42}`, 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTSFromAttr(tt.input)
			if got != tt.expected {
				t.Errorf("extractTSFromAttr(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}
