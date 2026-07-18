// Package search — fast_path.go: M14.Y4 simple-query fast-path gate.
//
// Skips d4_query_rewrite (avg 781 ms, 58% of search latency) and
// d7_cot_decompose (avg 294 ms, 22%) for queries that are simple factual
// lookups. Together these two LLM stages account for ~80% of average
// search latency but add zero recall value for simple one-fact questions.
//
// Design invariants:
//   - Env-gated (MEMDB_SEARCH_FAST_PATH=1, default OFF). When unset, the
//     pipeline is bytewise identical to the current default — no regression
//     risk without explicit opt-in.
//   - Uses a compile-time regex (no per-call alloc). Single goroutine-safe
//     compiled var at package init via regexp.MustCompile.
//   - Returns one of three metric outcomes:
//     enabled        — fast-path engaged (simple query + env ON)
//     disabled       — env unset/off (normal pipeline every time)
//     complex_query  — env ON but query failed simplicity gate
//   - DEFAULT OFF: operators enable via MEMDB_SEARCH_FAST_PATH=1 in .env.
//     A/B before widening: quality regression possible on edge queries that
//     look simple but benefit from rewriting (e.g. "last meeting" w/o date).
//
// Quality risk: queries that look simple but use relative temporal refs
// ("last summer", "recently") benefit from D4's date-anchoring. A/B is
// mandatory before enabling on production traffic. See PR body.
package search

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// fastPathEnvVar is the env flag that enables the simple-query fast-path.
// Default OFF — set to "1" or "true" to enable.
const fastPathEnvVar = "MEMDB_SEARCH_FAST_PATH"

// fastPathMaxQueryLen is the character-length cap for a query to be
// classified as simple. Queries longer than this are always sent through
// the full pipeline — they are likely multi-clause or context-rich.
const fastPathMaxQueryLen = 80

// simpleQueryRe matches queries that are likely to be simple factual
// one-shot lookups: short question-word openers OR bare noun-phrases.
//
// True-positive patterns (should engage fast-path):
//   - "When did X happen?"           — single temporal fact
//   - "What is Caroline's job?"      — attribute lookup
//   - "Who is Marcus?"               — identity lookup
//   - "Where does Sophia live?"      — location fact
//   - "How many cats does she have?" — count fact
//   - "Which school did he attend?"  — single-hop entity
//   - "Caroline's pets"              — bare noun-phrase (no verb)
//
// False-positive suppression (must stay complex):
//   - Queries with " and " conjunction → likely multi-part → stay complex
//   - Queries with "; " → multi-clause → stay complex
//   - Queries with two question marks → compound question → stay complex
//
// The regex is intentionally broad on starters: we rely on the length cap
// (fastPathMaxQueryLen) + the exclusion post-check below to suppress
// misclassifications.
var simpleQueryRe = regexp.MustCompile(
	`(?i)^\s*(when\b|where\b|who\b|what\b|which\b|how many\b|how long\b|how old\b|is\b|was\b|did\b|does\b|do\b)`,
)

// fastPathEnabled reads MEMDB_SEARCH_FAST_PATH. Default OFF.
func fastPathEnabled() bool {
	v := os.Getenv(fastPathEnvVar)
	return v == "1" || v == "true"
}

// isSimpleFactualQuery returns true when q looks like a single-fact lookup
// that does not benefit from LLM query rewriting or CoT decomposition.
//
// Rules (all must pass):
//  1. Length <= fastPathMaxQueryLen characters.
//  2. No " and " conjunction (suggests multi-part query).
//  3. No ";" character (suggests multi-clause).
//  4. At most one "?" character (two "?" = compound question).
//  5. Starts with a recognised question-word OR is a bare noun-phrase
//     (no question mark → statement / noun-phrase lookup).
//
// The function is safe for concurrent calls — all state is read-only.
func isSimpleFactualQuery(q string) bool {
	if len(q) > fastPathMaxQueryLen {
		return false
	}
	// Multi-part conjunction check (case-insensitive).
	if strings.Contains(strings.ToLower(q), " and ") {
		return false
	}
	// Multi-clause separator.
	if strings.ContainsRune(q, ';') {
		return false
	}
	// Compound question: more than one "?".
	if strings.Count(q, "?") > 1 {
		return false
	}
	// Either starts with a question-word OR is a bare noun-phrase (no "?").
	if simpleQueryRe.MatchString(q) {
		return true
	}
	// Bare noun-phrase: no question mark at all → treat as simple lookup.
	if !strings.ContainsRune(q, '?') {
		return true
	}
	return false
}

// fastPathStages returns the pipeline stage list with d4_query_rewrite and
// d7_cot_decompose removed.  All other stages are identical to defaultStages.
// Must be kept in sync with defaultStages() in service.go — a drift will
// not cause a build error but will cause a runtime difference.
//
// Note: stageNames (pipeline.go) is for metric pre-registration of the full
// default pipeline; fast-path stages are a strict subset so no update needed.
func (s *SearchService) fastPathStages() []stage {
	all := s.defaultStages()
	out := make([]stage, 0, len(all))
	for _, st := range all {
		switch st.Name() {
		case "d4_query_rewrite", "d7_cot_decompose":
			// skipped on simple factual path
		default:
			out = append(out, st)
		}
	}
	return out
}

// --- Metrics ---

var (
	fastPathMxOnce sync.Once
	fastPathMxInst *fastPathMetrics
)

type fastPathMetrics struct {
	Engaged  metric.Int64Counter
	FastFail metric.Int64Counter
}

func fastPathMx() *fastPathMetrics {
	fastPathMxOnce.Do(func() {
		m := otel.Meter("memdb-go/search")
		eng, _ := m.Int64Counter("memdb.search.fast_path_engaged_total",
			metric.WithDescription("M14.Y4 fast-path gate outcomes: enabled=fast-path took effect, disabled=env off, complex_query=env on but query too complex"))
		ff, _ := m.Int64Counter("memdb.search.fast_fail_total",
			metric.WithDescription("M14.Y4.1 early-return before pipeline assembly: reason=empty_cube means cube has 0 activated entries, skipping all LLM stages (~2s savings per call)"))
		fastPathMxInst = &fastPathMetrics{Engaged: eng, FastFail: ff}

		// Pre-register all outcome labels at zero so dashboards see
		// every series from container start before the first search fires.
		ctx := context.Background()
		for _, oc := range []string{"enabled", "disabled", "complex_query"} {
			eng.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
		ff.Add(ctx, 0, metric.WithAttributes(attribute.String("reason", "empty_cube")))
	})
	return fastPathMxInst
}

// recordFastPathOutcome emits the fast_path_engaged_total counter.
// outcome must be one of "enabled", "disabled", "complex_query".
func recordFastPathOutcome(ctx context.Context, outcome string) {
	fastPathMx().Engaged.Add(ctx, 1,
		metric.WithAttributes(attribute.String("outcome", outcome)))
}

// recordFastFail emits the fast_fail_total counter with the given reason label.
// Currently the only reason is "empty_cube".
func recordFastFail(ctx context.Context, reason string) {
	fastPathMx().FastFail.Add(ctx, 1,
		metric.WithAttributes(attribute.String("reason", reason)))
}
