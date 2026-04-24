package handlers

// chat_canary_test.go — unit tests for canaryFactualForUser.
// Covers: edge values (0%/100%), stickiness, distribution at 10%, empty user_id.

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

func TestCanaryFactualForUser_ZeroPct_NeverSelected(t *testing.T) {
	ids := []string{"alice", "bob", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
	for _, id := range ids {
		if canaryFactualForUser(id, 0) {
			t.Errorf("pct=0: user %q should never be selected, but was", id)
		}
	}
}

func TestCanaryFactualForUser_HundredPct_AlwaysSelected(t *testing.T) {
	ids := []string{"alice", "bob", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
	for _, id := range ids {
		if !canaryFactualForUser(id, 100) {
			t.Errorf("pct=100: user %q should always be selected, but was not", id)
		}
	}
}

func TestCanaryFactualForUser_Sticky_SameIDSameDecision(t *testing.T) {
	ids := []string{"user-1", "user-42", "user-abc", "user-xyz-9000", ""}
	for _, id := range ids {
		first := canaryFactualForUser(id, 10)
		for i := 0; i < 4; i++ {
			if got := canaryFactualForUser(id, 10); got != first {
				t.Errorf("sticky: user_id=%q call %d returned %v, first call returned %v",
					id, i+1, got, first)
			}
		}
	}
}

func TestCanaryFactualForUser_TenPct_DistributionWithinTolerance(t *testing.T) {
	const (
		n       = 10_000
		pct     = 10
		wantMin = 800  // 8%
		wantMax = 1200 // 12%
	)

	selected := 0
	rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic test RNG
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("user-%d-%d", i, rng.Uint64())
		if canaryFactualForUser(id, pct) {
			selected++
		}
	}

	t.Logf("10000 random ids @ pct=%d → %d selected = %.2f%%", pct, selected, float64(selected)/float64(n)*100)

	if selected < wantMin || selected > wantMax {
		t.Errorf("distribution out of tolerance: got %d/%d (%.2f%%), want [%d, %d]",
			selected, n, float64(selected)/float64(n)*100, wantMin, wantMax)
	}
}

func TestCanaryFactualForUser_EmptyUserID_Deterministic(t *testing.T) {
	// sha256("") → e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	// First 4 bytes: 0xe3, 0xb0, 0xc4, 0x42 → big-endian uint32 = 3820012610
	// 3820012610 % 100 = 10 — this is NOT in [0, 10) so empty user_id is outside the default 10% bucket.
	// pct=10 → false  (bucket=10, not < 10)
	// pct=11 → true   (bucket=10, 10 < 11)
	if canaryFactualForUser("", 10) {
		t.Error("empty user_id with pct=10: expected false (bucket=10, not < 10)")
	}
	if !canaryFactualForUser("", 11) {
		t.Error("empty user_id with pct=11: expected true (bucket=10, 10 < 11)")
	}
}

func TestCanaryFactualForUser_NegativePct_NeverSelected(t *testing.T) {
	// The canary receives clamped values from config, but test the guard directly.
	if canaryFactualForUser("alice", -1) {
		t.Error("pct=-1 should return false (treated as 0)")
	}
}

func TestCanaryFactualForUser_OverHundredPct_AlwaysSelected(t *testing.T) {
	if !canaryFactualForUser("alice", 101) {
		t.Error("pct=101 should return true (treated as 100)")
	}
}
