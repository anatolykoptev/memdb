// Package rerank — staged backend selection (M12.6).
//
// Resolves which StagedBackend (LLM / CE / Hybrid) actually serves a
// given Rerank invocation, following the env / availability precedence
// rules. Lives next to staged_backend.go (interface + impls) and
// staged.go (strategy entrypoint) so the three concerns stay in
// disjoint files: types ↔ logic ↔ strategy.
package rerank

// backendChoice carries the resolved backend plus enough context for
// the caller to fire the right outcome label / WARN message.
type backendChoice struct {
	// backend is the StagedBackend that will run; nil when nothing is
	// usable (caller treats as "skip staged" with WARN).
	backend StagedBackend
	// requested is the backend name the user asked for (env / explicit
	// override) — one of "ce" | "llm" | "hybrid" | "" (unset → default).
	requested string
	// fallback is true when the resolved backend differs from the
	// requested one (CE asked → LLM resolved, etc). Caller logs WARN
	// and uses outcome=fallback on the staged.backend_total counter.
	fallback bool
}

// pickBackend resolves the StagedBackend for this Rerank invocation
// and tells the caller whether the resolution is a fallback path.
//
// Precedence:
//  1. Explicit s.Backend wins (used by tests / advanced consumers).
//     fallback=false (caller asked for it explicitly, no degradation).
//  2. MEMDB_STAGED_BACKEND env in {ce, llm, hybrid}.
//     fallback=true when the requested backend is unavailable but a
//     different one can still run.
//  3. Default policy (env unset / unknown): ce when RerankClient is
//     wired and Available; else llm. fallback=false (no preference,
//     no degradation).
//
// backend=nil only when the requested backend genuinely cannot run
// (e.g. ce requested but no client wired, AND llm has no APIURL).
// Caller treats backend=nil as "skip staged" + WARN.
func (s Staged) pickBackend() backendChoice {
	if s.Backend != nil {
		return backendChoice{backend: s.Backend, requested: s.Backend.Name()}
	}
	requested := stagedBackendName()
	ceAvail := s.RerankClient != nil && s.RerankClient.Available()
	llmAvail := s.Config.APIURL != ""
	threshold := stagedCEThreshold()

	ce := func() StagedBackend { return CEBackend{Client: s.RerankClient, CEThreshold: threshold} }
	llm := func() StagedBackend { return LLMBackend{Config: s.Config} }
	hybrid := func() StagedBackend {
		return HybridBackend{Client: s.RerankClient, Config: s.Config, CEThreshold: threshold}
	}

	switch requested {
	case "ce":
		if ceAvail {
			return backendChoice{backend: ce(), requested: "ce"}
		}
		// CE asked, unavailable: fall back to LLM (NOT silent — caller
		// emits WARN). Better than skipping staged entirely.
		if llmAvail {
			return backendChoice{backend: llm(), requested: "ce", fallback: true}
		}
		return backendChoice{requested: "ce"}
	case "llm":
		if llmAvail {
			return backendChoice{backend: llm(), requested: "llm"}
		}
		return backendChoice{requested: "llm"}
	case "hybrid":
		if ceAvail && llmAvail {
			return backendChoice{backend: hybrid(), requested: "hybrid"}
		}
		// Degrade: prefer LLM-only over disabling.
		if llmAvail {
			return backendChoice{backend: llm(), requested: "hybrid", fallback: true}
		}
		if ceAvail {
			return backendChoice{backend: ce(), requested: "hybrid", fallback: true}
		}
		return backendChoice{requested: "hybrid"}
	}
	// Unset / unknown env. Default policy: prefer CE when wired, else
	// LLM. requested="" so the WARN/metric path treats it as the
	// implicit default rather than a fallback.
	if ceAvail {
		return backendChoice{backend: ce(), requested: ""}
	}
	if llmAvail {
		return backendChoice{backend: llm(), requested: ""}
	}
	return backendChoice{}
}
