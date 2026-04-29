// Package search — counting_classifier.go: cardinality-query detection and
// top-k boost (Task #100).
//
// Evidence: 5/26 chat-50 fails were WRONG_ANSWER_NO_RECALL counting queries.
// Examples:
//   "How many children does Melanie have?" → truth=3, pred="two younger" (1 missing in top-K)
//   "How many beach trips in 2023?"        → truth=2, pred="once or twice" (couldn't aggregate)
//
// Root cause: a counting question requires ALL mentions of the counted entity to
// reach the LLM so it can aggregate them.  With the default top-K of 10–20
// only a subset arrives, causing under-counting.
//
// Fix: when the query matches a counting pattern, inflate p.TopK to at least
// countingTopK() (default 30, env MEMDB_COUNTING_TOPK).  The boost is applied
// before computeBudget so the inflated value propagates to every DB fan-out
// sub-limit.  Default ON — this is a recall bug fix, not an opt-in feature.
//
// Opt-out: set MEMDB_COUNTING_TOPK=0.
package search

import (
	"regexp"
)

// countingPatterns is the ordered set of regexps that identify counting /
// cardinality questions. Patterns are compiled once at package init.
//
// Design notes:
//   - \bhow\s+many\b — the canonical cardinality starter; restricted to a word
//     boundary so "somehow many" does not match.
//   - \bhow\s+often\b — frequency questions ("how often does she visit?").
//   - \bhow\s+much\b — quantity questions ("how much did he spend?").
//   - \b(count|number\s+of|total)\b — explicit aggregation vocabulary.
//
// cat2QueryRe in pipeline_multihop.go already handles the temporal subset
// "how many months/years/days/weeks/times ago" with a threshold lowering.
// This classifier is orthogonal — it covers the cardinality dimension without
// temporal restriction.
var countingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bhow\s+many\b`),
	regexp.MustCompile(`(?i)\bhow\s+often\b`),
	regexp.MustCompile(`(?i)\bhow\s+much\b`),
	regexp.MustCompile(`(?i)\b(count|number\s+of|total)\b`),
}

// isCountingQuery returns true when q contains a cardinality / counting phrase.
// Exported so tests can pin the heuristic without going through the full pipeline.
func isCountingQuery(q string) bool {
	for _, re := range countingPatterns {
		if re.MatchString(q) {
			return true
		}
	}
	return false
}

// defaultCountingTopK is the minimum top-K budget applied to counting queries
// when their current TopK is lower.  Value 30 is chosen to cover typical
// cardinality scenarios (2–5 distinct mentions scattered across time) with
// comfortable headroom.
const defaultCountingTopK = 30

// countingTopK returns the configured counting top-K minimum.
//
// Env: MEMDB_COUNTING_TOPK in [0, 200].
//   - 0  → disable the boost entirely (opt-out).
//   - 1..200 → the minimum TopK to enforce for counting queries.
//   - unset / out-of-range → 30 (default).
func countingTopK() int {
	return parseEnvInt("MEMDB_COUNTING_TOPK", 0, 200, defaultCountingTopK)
}

// applyCountingBoost raises st.Params.TopK to at least countingTopK() when
// the query is a counting question and the boost is not disabled (MEMDB_COUNTING_TOPK≠0).
//
// Returns "boosted" when the limit was raised, "skipped" otherwise.
// The return value is used for metric tagging only.
//
// Semantics — only-raise-never-lower:
//   - TopK already ≥ countingTopK() → keep (caller already has a wider window).
//   - countingTopK() == 0 → boost disabled; return "skipped".
//   - isCountingQuery returns false → return "skipped".
func applyCountingBoost(st *pipelineState) string {
	boostedK := countingTopK()
	if boostedK == 0 {
		return "skipped"
	}
	if !isCountingQuery(st.Params.Query) {
		return "skipped"
	}
	if st.Params.TopK < boostedK {
		st.Params.TopK = boostedK
	}
	return "boosted"
}
