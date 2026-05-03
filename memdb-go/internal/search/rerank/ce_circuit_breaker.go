package rerank

// ce_circuit_breaker.go — per-cube CE low_spread circuit breaker.
//
// Forensic Karpathy r2 (2026-05-01) found CE rerank fired 198× over a
// chat-50 run but 154 (78%) returned reason="low_spread" — math fallback
// re-sorted by cosine = identical to the cosine result already produced
// upstream. judge_changed_top1=0 across all 198 calls. Pure CPU waste:
// ~150ms/query CE budget burned for zero ranking signal.
//
// This breaker tracks the last N CE outcomes per cube_id. When the
// low_spread rate over that window exceeds a threshold, subsequent CE
// calls for the same cube short-circuit straight to the math fallback
// (cosine on QueryVec) — avoiding the live HTTP roundtrip entirely.
//
// Self-recovering: every CE call records its outcome, so cubes whose CE
// quality returns (e.g. corpus changes, model swap) flip back below the
// threshold and the breaker closes again. No timed reset, no global
// state — strictly per-cube ring buffer.
//
// This is parallel to the existing global MEMDB_RERANK_CIRCUIT breaker
// (server_init_search.go) which gates rerank on consecutive HTTP
// failures across the whole process. The per-cube variant is targeted
// at semantic-quality cubes where CE adds no signal but the live path is
// healthy — neither breaker subsumes the other.

import (
	"sync"
	"sync/atomic"
)

const (
	// ceCircuitWindowSize is the per-cube ring-buffer length of recent CE
	// outcomes. 50 ≈ a small chat-eval batch — large enough to smooth
	// transient spikes, small enough to react within one user session.
	ceCircuitWindowSize = 50

	// ceCircuitOpenThreshold is the low_spread fraction above which the
	// breaker opens for the cube. Tuned 2026-05-02 (Run #17): 0.5 → 0.85.
	// Initial 0.5 was too aggressive — Run #17 showed cat2/cat4 F1 collapse
	// (-23% / -46%) когда breaker skipped CE on cubes where retrieval
	// hit@k=0.9 but cosine ranking ordered wrong fact first. CE was
	// providing real discrimination on those cubes; breaker masked it.
	// 0.85 catches genuinely dead cubes (truly random CE output) while
	// preserving CE on cubes where it matters even if signal is weak.
	ceCircuitOpenThreshold = 0.85

	// ceCircuitMinCallsToOpen is the minimum sample size before the rate
	// is evaluated. Tuned 2026-05-02: 10 → 30. Larger sample requires
	// sustained pattern before opening — single eval batch (50 calls) at
	// rate 0.55 must show 30+ low_spread before breaker engages, not 10.
	ceCircuitMinCallsToOpen = 30
)

// ceCubeCircuit tracks low_spread CE outcomes per cube_id and opens
// (skips CE) when the cube's CE consistently fails to discriminate.
// Sliding window per cube; self-resolves when fresh calls flip the rate
// below threshold.
type ceCubeCircuit struct {
	mu      sync.Mutex
	perCube map[string]*cubeCircuitState
}

// cubeCircuitState is a single cube's ring buffer of CE outcomes.
// outcomes[i]=true means the i-th call ended in math fallback with
// reason=low_spread. lowSpreadCount mirrors the count of true entries
// for O(1) rate computation.
type cubeCircuitState struct {
	mu             sync.Mutex
	outcomes       [ceCircuitWindowSize]bool
	pos            int
	filled         int
	lowSpreadCount atomic.Int32
}

// globalCECircuit is the package-level breaker. Lifetime = process.
// Map allocation cost is one entry per distinct cube_id seen — bounded
// by the deployment's tenant count.
var globalCECircuit = newCECubeCircuit()

func newCECubeCircuit() *ceCubeCircuit {
	return &ceCubeCircuit{perCube: make(map[string]*cubeCircuitState)}
}

// shouldSkipCE returns true when this cube's recent CE history says CE
// adds no signal (low_spread rate over the last filled window > threshold,
// with at least ceCircuitMinCallsToOpen samples). Empty cubeID always
// returns false — no per-cube tracking possible.
func (c *ceCubeCircuit) shouldSkipCE(cubeID string) bool {
	if cubeID == "" {
		return false
	}
	c.mu.Lock()
	s := c.perCube[cubeID]
	c.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	filled := s.filled
	s.mu.Unlock()
	if filled < ceCircuitMinCallsToOpen {
		return false
	}
	rate := float64(s.lowSpreadCount.Load()) / float64(filled)
	return rate > ceCircuitOpenThreshold
}

// recordOutcome appends an outcome to the cube's ring buffer.
// lowSpread=true means the live CE call discriminated poorly (math
// fallback fired with reason=low_spread). Anything else (healthy CE,
// degraded transport, low_quality, bypass_cosine) records false.
//
// Empty cubeID is a no-op — the breaker only operates on identified cubes.
func (c *ceCubeCircuit) recordOutcome(cubeID string, lowSpread bool) {
	if cubeID == "" {
		return
	}
	c.mu.Lock()
	s, ok := c.perCube[cubeID]
	if !ok {
		s = &cubeCircuitState{}
		c.perCube[cubeID] = s
	}
	c.mu.Unlock()

	s.mu.Lock()
	old := s.outcomes[s.pos]
	s.outcomes[s.pos] = lowSpread
	s.pos = (s.pos + 1) % ceCircuitWindowSize
	if s.filled < ceCircuitWindowSize {
		s.filled++
	}
	s.mu.Unlock()

	// Maintain lowSpreadCount as the count of `true` entries currently
	// in the buffer. Net delta = (new entry adds) - (overwritten entry).
	// On the first ceCircuitWindowSize calls `old` was the zero value
	// (false), which matches the "ring not yet full" semantics.
	switch {
	case lowSpread && !old:
		s.lowSpreadCount.Add(1)
	case !lowSpread && old:
		s.lowSpreadCount.Add(-1)
	}
}
