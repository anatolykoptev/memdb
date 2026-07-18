// Package search — temporal_augment.go: M11 F7 temporal-index augmentation
// stage.
//
// Atomic facts (F8) carry an `event_dates` JSON array on properties; F7
// indexes that field (migrations/0024_event_dates.sql) and surfaces it here:
//
//  1. Detect a date hint in the query — explicit year ("in 2024"), explicit
//     ISO date ("on 2024-03-15"), or a relative phrase ("last month",
//     "yesterday"). Hint resolves to a [start, end] inclusive ISO range.
//  2. If found, query Postgres for memories whose event_dates intersect the
//     range (db.SearchMemoriesByDateRange) and boost the matching IDs in
//     st.TextMerged by temporalBoost (default 0.15, env-tunable).
//  3. If no hint, the stage is a no-op (skipped) — zero extra DB work, the
//     latency budget is respected.
//
// Targeted at LoCoMo cat-2 ("When did X happen") and cat-4 ("How long ago X")
// query buckets. cat-4 detection lives in tuning.go (isCat4Query).
//
// Soft-fail contract (pipeline.go): a DB error returns err so the orchestrator
// records outcome=error; the pipeline always continues with the original
// (un-boosted) TextMerged.
package search

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// temporalEnvFlag is the env var that gates the F7 stage. Default = ON.
// Set MEMDB_F7_TEMPORAL=0 / false / off to disable without a code change.
const temporalEnvFlag = "MEMDB_F7_TEMPORAL"

// temporalBoostDefault is the additive score boost applied to MergedResult
// items whose event_dates intersect the query range. Empirically chosen so a
// strong text/vector match still wins over a weak vector+temporal match;
// override via MEMDB_F7_TEMPORAL_BOOST in [0, 1].
const temporalBoostDefault = 0.15

// temporalDBLimit caps the per-query DB lookup. 200 covers the typical
// LoCoMo "year-of-X" question without blowing the latency budget.
const temporalDBLimit = 200

// temporalMinYear / temporalMaxYear bound the year hints we accept; a stray
// "2099" in a query never resolves to a date range.
const (
	temporalMinYear = 1900
	temporalMaxYear = 2099
)

// isoDateLayoutSearch is the Go time-format layout for "YYYY-MM-DD".
const isoDateLayoutSearch = "2006-01-02"

// yearRe matches any 4-digit year token in [1900, 2099].
var yearRe = regexp.MustCompile(`\b(19[0-9]{2}|20[0-9]{2})\b`)

// isoDateRe matches a YYYY-MM-DD literal anywhere in the query.
var isoDateRe = regexp.MustCompile(`\b(19[0-9]{2}|20[0-9]{2})-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])\b`)

// relativePhraseRe matches the common English relative-time phrases we
// resolve against time.Now(). Kept narrow to avoid false positives.
var relativePhraseRe = regexp.MustCompile(`(?i)\b(yesterday|today|last (?:week|month|year)|this (?:week|month|year))\b`)

// temporalRange is the resolved [Start, End] window for a query. Both bounds
// are "YYYY-MM-DD" strings; either may be empty (open-ended on that side).
type temporalRange struct {
	Start string
	End   string
}

// temporalEnabled honours MEMDB_F7_TEMPORAL. Defaults to true.
func temporalEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(temporalEnvFlag)))
	switch v {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// temporalBoost reads MEMDB_F7_TEMPORAL_BOOST. Falls back to the default on
// invalid / out-of-range input.
func temporalBoost() float64 {
	return parseEnvFloat("MEMDB_F7_TEMPORAL_BOOST", 0, 1, temporalBoostDefault)
}

// extractTemporalRange inspects q for a date hint and returns the resolved
// inclusive [Start, End] range, plus ok=true when a hint was found. now is
// passed in so tests can pin "today". Resolution priority:
//
//  1. ISO date literal — single-day range.
//  2. Relative phrase ("yesterday", "last month", ...) — period range.
//  3. Bare year — full-year range.
func extractTemporalRange(q string, now time.Time) (temporalRange, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return temporalRange{}, false
	}
	if m := isoDateRe.FindString(q); m != "" {
		return temporalRange{Start: m, End: m}, true
	}
	if m := relativePhraseRe.FindString(q); m != "" {
		if r, ok := resolveRelative(m, now); ok {
			return r, true
		}
	}
	if m := yearRe.FindString(q); m != "" {
		y, err := strconv.Atoi(m)
		if err == nil && y >= temporalMinYear && y <= temporalMaxYear {
			return temporalRange{Start: m + "-01-01", End: m + "-12-31"}, true
		}
	}
	return temporalRange{}, false
}

// resolveRelative turns a phrase like "last month" into a concrete ISO range.
func resolveRelative(phrase string, now time.Time) (temporalRange, bool) {
	now = now.UTC()
	switch strings.ToLower(phrase) {
	case "today":
		d := now.Format(isoDateLayoutSearch)
		return temporalRange{Start: d, End: d}, true
	case "yesterday":
		d := now.AddDate(0, 0, -1).Format(isoDateLayoutSearch)
		return temporalRange{Start: d, End: d}, true
	case "this week":
		// Rolling 7 days, inclusive of today — calendar-week boundaries vary
		// by locale, "rolling" is the safer default.
		end := now.Format(isoDateLayoutSearch)
		start := now.AddDate(0, 0, -6).Format(isoDateLayoutSearch)
		return temporalRange{Start: start, End: end}, true
	case "last week":
		end := now.AddDate(0, 0, -7).Format(isoDateLayoutSearch)
		start := now.AddDate(0, 0, -13).Format(isoDateLayoutSearch)
		return temporalRange{Start: start, End: end}, true
	case "this month":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		last := first.AddDate(0, 1, -1)
		return temporalRange{Start: first.Format(isoDateLayoutSearch), End: last.Format(isoDateLayoutSearch)}, true
	case "last month":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		last := first.AddDate(0, 1, -1)
		return temporalRange{Start: first.Format(isoDateLayoutSearch), End: last.Format(isoDateLayoutSearch)}, true
	case "this year":
		y := strconv.Itoa(now.Year())
		return temporalRange{Start: y + "-01-01", End: y + "-12-31"}, true
	case "last year":
		y := strconv.Itoa(now.Year() - 1)
		return temporalRange{Start: y + "-01-01", End: y + "-12-31"}, true
	}
	return temporalRange{}, false
}

// temporalRangeSearcher is the subset of *db.Postgres needed by F7. The
// search-package postgresClient interface stays minimal; we type-assert to
// pick up the optional method when running against a real Postgres.
type temporalRangeSearcher interface {
	SearchMemoriesByDateRange(ctx context.Context, userName, start, end string, limit int) ([]db.TemporalMatch, error)
}

// stageTemporalAugment is the F7 search stage. Wired into defaultStages
// between merge_candidates (which populates st.TextMerged) and d2_graph_expand
// so the boosted scores feed the graph walker's seed selection too.
//
// No-op (skip) when:
//   - MEMDB_F7_TEMPORAL=0 (disabled)
//   - state.TextMerged empty (nothing to boost)
//   - state.Params.UserName empty (cube scoping requires it)
//   - extractTemporalRange returns no hint
//   - postgres mock doesn't implement SearchMemoriesByDateRange (unit tests)
//
// On a hit the stage records:
//   - memdb.temporal.queries_with_dates_total — counter (one per hit)
//   - memdb.temporal.boosts_applied_total     — counter, summed boost count
func (s *SearchService) stageTemporalAugment(ctx context.Context, st *pipelineState) error {
	if !temporalEnabled() {
		st.skip("temporal_augment")
		return nil
	}
	if len(st.TextMerged) == 0 || st.Params.UserName == "" || s.postgres == nil {
		st.skip("temporal_augment")
		return nil
	}
	rng, ok := extractTemporalRange(st.Params.Query, time.Now())
	if !ok {
		st.skip("temporal_augment")
		return nil
	}
	pg, ok := s.postgres.(temporalRangeSearcher)
	if !ok {
		st.skip("temporal_augment")
		return nil
	}

	mx := temporalMx()
	mx.QueriesWithDates.Add(ctx, 1)

	matches, err := pg.SearchMemoriesByDateRange(ctx, st.Params.UserName, rng.Start, rng.End, temporalDBLimit)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}
	matchIDs := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		matchIDs[m.ID] = struct{}{}
	}

	boost := temporalBoost()
	applied := int64(0)
	for i := range st.TextMerged {
		if _, ok := matchIDs[st.TextMerged[i].ID]; !ok {
			continue
		}
		st.TextMerged[i].Score += boost
		applied++
	}
	if applied > 0 {
		mx.BoostsApplied.Add(ctx, applied)
		// Keep the post-boost order monotonic — downstream stages assume
		// TextMerged is sorted by Score DESC.
		sort.SliceStable(st.TextMerged, func(i, j int) bool {
			return st.TextMerged[i].Score > st.TextMerged[j].Score
		})
	}
	return nil
}

// temporalMetrics holds the F7 telemetry instruments. Pre-registered at zero
// in temporalMx() so dashboards see the series from container start.
type temporalMetrics struct {
	QueriesWithDates metric.Int64Counter
	BoostsApplied    metric.Int64Counter
}

var (
	temporalMxOnce sync.Once
	temporalMxInst *temporalMetrics
)

func temporalMx() *temporalMetrics {
	temporalMxOnce.Do(func() {
		m := otel.Meter("memdb-go/search")
		qd, _ := m.Int64Counter("memdb.temporal.queries_with_dates_total",
			metric.WithDescription("F7: search queries that resolved to a temporal range and triggered the augmentation stage"))
		ba, _ := m.Int64Counter("memdb.temporal.boosts_applied_total",
			metric.WithDescription("F7: count of MergedResult items whose score was boosted by the temporal augmentation stage"))
		temporalMxInst = &temporalMetrics{QueriesWithDates: qd, BoostsApplied: ba}
		// Pre-register at zero so Prometheus sees the series from start.
		ctx := context.Background()
		qd.Add(ctx, 0)
		ba.Add(ctx, 0)
	})
	return temporalMxInst
}
