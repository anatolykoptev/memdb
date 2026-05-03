package rerank

import "testing"

// fillOutcomes records n outcomes against the given cube via a fresh
// breaker so each test starts from a clean state.
func fillOutcomes(c *ceCubeCircuit, cube string, low, healthy int) {
	for i := 0; i < low; i++ {
		c.recordOutcome(cube, true)
	}
	for i := 0; i < healthy; i++ {
		c.recordOutcome(cube, false)
	}
}

func TestCECircuit_EmptyHistory_DoesNotSkip(t *testing.T) {
	c := newCECubeCircuit()
	if c.shouldSkipCE("cubeA") {
		t.Fatal("shouldSkipCE on empty history = true; want false")
	}
}

func TestCECircuit_EmptyCubeID_NeverSkips(t *testing.T) {
	c := newCECubeCircuit()
	// Even with many low_spread outcomes recorded against an empty
	// cube_id, shouldSkipCE("") must always return false (no per-cube
	// tracking is possible without an identifier).
	for i := 0; i < ceCircuitWindowSize; i++ {
		c.recordOutcome("", true)
	}
	if c.shouldSkipCE("") {
		t.Fatal("shouldSkipCE(\"\") = true; want false (no tracking possible)")
	}
}

func TestCECircuit_BelowMinCalls_DoesNotSkip(t *testing.T) {
	c := newCECubeCircuit()
	// 5 low_spread calls — rate is 1.0 but sample size is below
	// ceCircuitMinCallsToOpen (10), so the breaker stays closed.
	fillOutcomes(c, "cubeA", 5, 0)
	if c.shouldSkipCE("cubeA") {
		t.Fatalf("shouldSkipCE with %d samples = true; want false (need ≥%d)",
			5, ceCircuitMinCallsToOpen)
	}
}

func TestCECircuit_AboveThreshold_Skips(t *testing.T) {
	c := newCECubeCircuit()
	// 6 low_spread + 4 healthy = rate 0.6 > 0.5 threshold, sample 10 ≥ min.
	fillOutcomes(c, "cubeA", 6, 4)
	if !c.shouldSkipCE("cubeA") {
		t.Fatal("shouldSkipCE at 60% low_spread rate = false; want true")
	}
}

func TestCECircuit_AtThreshold_DoesNotSkip(t *testing.T) {
	c := newCECubeCircuit()
	// Exactly 50% — strictly greater than threshold required.
	fillOutcomes(c, "cubeA", 5, 5)
	if c.shouldSkipCE("cubeA") {
		t.Fatal("shouldSkipCE at exactly 50% = true; want false (strict >)")
	}
}

func TestCECircuit_PerCubeIsolation(t *testing.T) {
	c := newCECubeCircuit()
	fillOutcomes(c, "cubeA", 8, 2) // 80% low_spread → open
	fillOutcomes(c, "cubeB", 1, 9) // 10% low_spread → closed
	if !c.shouldSkipCE("cubeA") {
		t.Fatal("cubeA should be open")
	}
	if c.shouldSkipCE("cubeB") {
		t.Fatal("cubeB should remain closed independent of cubeA")
	}
}

func TestCECircuit_RecoveryAfterFreshHealthy(t *testing.T) {
	c := newCECubeCircuit()
	// Open the breaker first.
	fillOutcomes(c, "cubeA", 8, 2)
	if !c.shouldSkipCE("cubeA") {
		t.Fatal("precondition: cubeA should be open after 8/2 fill")
	}
	// Push the ring buffer with enough fresh healthy outcomes that the
	// rate drops below threshold. After ceCircuitWindowSize healthy
	// calls every old low_spread entry has been overwritten.
	for i := 0; i < ceCircuitWindowSize; i++ {
		c.recordOutcome("cubeA", false)
	}
	if c.shouldSkipCE("cubeA") {
		t.Fatal("cubeA should close after a window of healthy outcomes")
	}
}

func TestCECircuit_RingBufferOverwrites(t *testing.T) {
	c := newCECubeCircuit()
	// Fill the buffer entirely with low_spread outcomes.
	for i := 0; i < ceCircuitWindowSize; i++ {
		c.recordOutcome("cubeA", true)
	}
	// Sanity: count == window size.
	c.mu.Lock()
	s := c.perCube["cubeA"]
	c.mu.Unlock()
	if got := s.lowSpreadCount.Load(); got != int32(ceCircuitWindowSize) {
		t.Fatalf("lowSpreadCount after full low fill = %d; want %d", got, ceCircuitWindowSize)
	}
	// Overwrite half with healthy — count should drop accordingly.
	half := ceCircuitWindowSize / 2
	for i := 0; i < half; i++ {
		c.recordOutcome("cubeA", false)
	}
	want := int32(ceCircuitWindowSize - half)
	if got := s.lowSpreadCount.Load(); got != want {
		t.Fatalf("lowSpreadCount after half-overwrite = %d; want %d", got, want)
	}
}
