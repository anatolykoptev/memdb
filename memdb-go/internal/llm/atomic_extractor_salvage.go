// Package llm — atomic_extractor_salvage.go: graceful-degradation fallback
// for the atomic-fact extractor.
//
// When ChatStructuredWithRaw exhausts its parse-retries on a malformed
// upstream response (trailing comma, unescaped quote, prose preamble like
// "Here's the JSON:", missing closing brace, etc.), this file salvages
// whatever facts can still be extracted instead of dropping the whole chunk.
//
// Three-tier fallback, mirroring the well-tested go-engine/llm + go-wp
// patterns (kitllm.ExtractJSON for fence/prose strip, regex per-line for
// shattered envelopes):
//
//  1. kitllm.ExtractJSON — strip ```json fences + extract first-to-last
//     brace substring → attempt full envelope Unmarshal.
//  2. Regex per-line {"id":..., "text":...} → per-fact Unmarshal — recovers
//     partial output even from fundamentally broken envelopes.
//  3. Empty result → caller treats the chunk as a hard failure.
//
// All paths emit memdb.atomic.salvage_total{outcome=recovered|empty|no_raw}
// so operators see the failure mode mix in /metrics.
package llm

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"

	kitllm "github.com/anatolykoptev/go-kit/llm"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// salvageMetricsOnce + salvageCtr lazy-init the salvage-outcome counter.
// Same lazy pattern used by temporalInvalidCounter so package-level metric
// init doesn't fire in unit tests that skip the OTel meter.
var (
	salvageMetricsOnce sync.Once
	salvageCtr         metric.Int64Counter
)

// salvageOutcomeCounter returns the singleton counter for graceful-degradation
// outcomes. Outcome label values:
//
//   - recovered: salvage produced ≥1 fact from a raw response that failed
//     strict JSON parse. Chunk is NOT lost; downstream embed/persist runs.
//   - empty: salvage matched no fact-shaped substrings. Chunk is dropped.
//   - no_raw: ChatStructuredWithRaw returned no upstream content (HTTP error
//     before body, or pre-flight client/target nil-check). Salvage skipped.
func salvageOutcomeCounter() metric.Int64Counter {
	salvageMetricsOnce.Do(func() {
		m := otel.Meter("memdb-go/llm")
		salvageCtr, _ = m.Int64Counter("memdb.atomic.salvage_total",
			metric.WithDescription("Atomic-extractor graceful-degradation outcomes after final ChatStructured parse failure (recovered|empty|no_raw)"))
	})
	return salvageCtr
}

// factObjectPattern matches a single-line JSON object containing both "id"
// and "text" keys — the minimum AtomicFact shape. Used by the tier-2 line
// scan to recover individual facts when the surrounding envelope is broken.
//
// Per-line because the prompt instructs LLMs to emit one fact per line; a
// balanced-brace parser would buy nothing extra and add complexity. Inner
// brace exclusion `[^{}]*` keeps the pattern non-greedy and prevents matching
// across object boundaries.
var factObjectPattern = regexp.MustCompile(`\{[^{}]*"id"\s*:\s*"[^"]*"[^{}]*"text"\s*:\s*"(?:[^"\\]|\\.)*"[^{}]*\}`)

// Tier constants for salvageWithTier — observable in tests, used to assert
// that tier-1 actually carries traffic when it can (without this, a bug
// where tier-1 silently always fails would still pass tier-2-driven tests).
const (
	salvageTierNone = 0 // nothing recovered
	salvageTierOne  = 1 // kitllm.ExtractJSON + envelope Unmarshal succeeded
	salvageTierTwo  = 2 // factObjectPattern per-fact regex recovered something
)

// salvageAtomicFacts is the graceful-degradation fallback. Three-tier
// recovery — see file-level docblock for the full contract.
//
// Returns the salvaged facts (post-empty-Text filter) or nil if nothing was
// recoverable. Caller is responsible for emitting the salvage_total metric
// with the appropriate outcome label.
func salvageAtomicFacts(raw string) []AtomicFact {
	facts, _ := salvageWithTier(raw)
	return facts
}

// salvageWithTier is the test-observable variant: returns (facts, tier) where
// tier ∈ {salvageTierOne, salvageTierTwo, salvageTierNone}. Production callers
// use salvageAtomicFacts which drops the tier.
func salvageWithTier(raw string) ([]AtomicFact, int) {
	cleaned := kitllm.ExtractJSON(raw)

	// Tier 1: full-envelope Unmarshal on the cleaned substring.
	if cleaned != "" {
		var env atomicFactsResponse
		if json.Unmarshal([]byte(cleaned), &env) == nil && len(env.Memory) > 0 {
			out := filterValidFacts(env.Memory)
			if len(out) > 0 {
				return out, salvageTierOne
			}
		}
	}

	// Tier 2: per-fact regex scan over the original raw string. We use the
	// raw (not cleaned) because Tier-1 substring may have lopped off facts
	// that survived inside otherwise-broken envelopes.
	matches := factObjectPattern.FindAllString(raw, -1)
	if len(matches) == 0 {
		return nil, salvageTierNone
	}
	out := make([]AtomicFact, 0, len(matches))
	for _, m := range matches {
		var f AtomicFact
		if err := json.Unmarshal([]byte(m), &f); err != nil {
			continue
		}
		f.Text = strings.TrimSpace(f.Text)
		if f.Text == "" {
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, salvageTierNone
	}
	return out, salvageTierTwo
}

// filterValidFacts drops facts with empty Text. Mirrors the inline check in
// ExtractAtomicFacts so salvaged output goes through the same gate as the
// happy-path facts.
func filterValidFacts(in []AtomicFact) []AtomicFact {
	out := make([]AtomicFact, 0, len(in))
	for _, f := range in {
		f.Text = strings.TrimSpace(f.Text)
		if f.Text == "" {
			continue
		}
		out = append(out, f)
	}
	return out
}
