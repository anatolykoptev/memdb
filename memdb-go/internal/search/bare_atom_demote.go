// Package search — bare_atom_demote.go: Pattern-B bare-token atomic poisoning fix.
//
// Problem: SPLADE / keyword retrieval gives 1-token docs ("Yes", "No", "7 years")
// a perfect score=1.000 because they exact-match the query token. The CE reranker
// can't fix this — it sees a short high-confidence doc and can't distinguish
// affirmative from negation. Gold is usually a longer narrative passage.
//
// Fix: after fusion but before the CE/cosine pass, scan the top-3 of the fused
// list.  If rank-1 is ≤ MEMDB_BARE_ATOM_MAX_TOKENS word-tokens (default 2) AND
// any of ranks 2-3 has ≥ 6 word-tokens AND is within MEMDB_BARE_ATOM_SCORE_MARGIN
// (default 0.05 = 5%) of rank-1's score → swap them.
//
// Env gates:
//
//	MEMDB_DEMOTE_BARE_ATOMS=0   — disable (default ON)
//	MEMDB_BARE_ATOM_MAX_TOKENS=2 — max word-tokens to classify a doc as bare atom
//	MEMDB_BARE_ATOM_MIN_LONG=6   — min word-tokens for the promoted replacement
//	MEMDB_BARE_ATOM_SCORE_MARGIN=0.05 — max relative score gap between rank-1 and
//	                                    the promoted candidate (fraction of rank-1 score)
package search

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/util/envcfg"
)

const (
	demoteBareAtomsEnvVar       = "MEMDB_DEMOTE_BARE_ATOMS"
	bareAtomMaxTokensEnvVar     = "MEMDB_BARE_ATOM_MAX_TOKENS"
	bareAtomMinLongEnvVar       = "MEMDB_BARE_ATOM_MIN_LONG"
	bareAtomScoreMarginEnvVar   = "MEMDB_BARE_ATOM_SCORE_MARGIN"

	defaultBareAtomMaxTokens   = 2
	defaultBareAtomMinLong     = 6
	defaultBareAtomScoreMargin = 0.05
)

// demoteBareAtomsEnabled returns true when bare-atom demotion is active.
// Default ON (env "0" / "false" disables).
func demoteBareAtomsEnabled() bool {
	return envcfg.Bool(demoteBareAtomsEnvVar, true)
}

// bareAtomMaxTokens returns the configurable word-token upper-bound for "bare atom".
func bareAtomMaxTokens() int {
	return envcfg.IntRange(bareAtomMaxTokensEnvVar, defaultBareAtomMaxTokens, 1, 10)
}

// bareAtomMinLong returns the configurable word-token lower-bound for "long enough".
func bareAtomMinLong() int {
	return envcfg.IntRange(bareAtomMinLongEnvVar, defaultBareAtomMinLong, 2, 100)
}

// bareAtomScoreMargin returns the allowed score gap fraction.
func bareAtomScoreMargin() float64 {
	return envcfg.FloatRange(bareAtomScoreMarginEnvVar, defaultBareAtomScoreMargin, 0.0, 1.0)
}

// wordTokenCount counts whitespace-separated tokens in s.
func wordTokenCount(s string) int {
	return len(strings.Fields(s))
}

// memoryText extracts the memory text from a MergedResult's Properties JSON.
// Returns empty string when the field is absent or Properties is unparseable.
func memoryText(r MergedResult) string {
	props := ParseProperties(r.Properties)
	if props == nil {
		return ""
	}
	if v, ok := props["memory"].(string); ok {
		return v
	}
	if v, ok := props["memory_content"].(string); ok {
		return v
	}
	return ""
}

// DemoteBareAtoms scans the top-3 candidates and demotes a bare-token rank-1
// doc when a longer candidate within the score margin exists at rank-2 or rank-3.
//
// The swap is score-preserving: the two items exchange positions; scores are not
// recomputed.  The heuristic fires at most once per call (first qualifying swap).
//
// Returns the (possibly reordered) slice unchanged when the guard conditions are
// not met or the feature is disabled.
func DemoteBareAtoms(ctx context.Context, results []MergedResult) []MergedResult {
	if !demoteBareAtomsEnabled() {
		recordBareAtomDemote(ctx, false)
		return results
	}
	if len(results) < 2 {
		recordBareAtomDemote(ctx, false)
		return results
	}

	maxTokens := bareAtomMaxTokens()
	minLong := bareAtomMinLong()
	margin := bareAtomScoreMargin()

	rank1Text := memoryText(results[0])
	if wordTokenCount(rank1Text) > maxTokens {
		// Rank-1 is not a bare atom — nothing to do.
		recordBareAtomDemote(ctx, false)
		return results
	}

	rank1Score := results[0].Score
	scanN := 3
	if len(results) < scanN {
		scanN = len(results)
	}

	for i := 1; i < scanN; i++ {
		candidate := results[i]
		// Score must be within margin of rank-1 (absolute gap / rank-1 score).
		// Guard against rank-1 score=0 to avoid division by zero.
		if rank1Score > 0 {
			relGap := (rank1Score - candidate.Score) / rank1Score
			if relGap > margin {
				continue
			}
		}
		if wordTokenCount(memoryText(candidate)) >= minLong {
			// Swap rank-1 and this candidate.
			results[0], results[i] = results[i], results[0]
			recordBareAtomDemote(ctx, true)
			return results
		}
	}

	recordBareAtomDemote(ctx, false)
	return results
}

// recordBareAtomDemote bumps the BareAtomDemoted counter with moved="true"|"false".
func recordBareAtomDemote(ctx context.Context, moved bool) {
	mx := searchMx()
	if mx.BareAtomDemoted == nil {
		return
	}
	label := "false"
	if moved {
		label = "true"
	}
	mx.BareAtomDemoted.Add(ctx, 1, metric.WithAttributes(attribute.String("moved", label)))
}
