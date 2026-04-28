// Package search — event_inject.go: M11 F3 search-time event injection.
//
// After candidate merge, we look up the top-N relevant user_events for the
// query and splice them into the text candidate stream as additional rows
// so they go through the same rerank/dedup chain as memory rows. Events
// already carry their own date anchor + tags so they tend to win cat-4
// (temporal) and cat-2 (multi-hop) head-to-heads where the LTM rows are
// drowned by everyday chatter.
//
// Selection strategy (cheap first, expensive last):
//   1. Date window — when the query has a temporal cutoff, fetch events
//      inside [cutoff - 7d, cutoff + 7d]. Direct btree lookup, no LLM.
//   2. Tag overlap — when SearchParams.Tags is non-empty, use them.
//      GIN-accelerated, also no LLM.
//   3. Cosine — fall back on the query embedding. HNSW lookup against the
//      halfvec index.
//
// We accept up to eventInjectTopN events per request (default 5). Each one
// becomes a synthetic MergedResult with a properties JSON shape that
// FormatMergedItems can consume — same fields as a real Memory row, just
// with memory_type="EventSummary" so callers can spot/filter them.
//
// Env gate: MEMDB_F3_EVENTS (shared with the extractor — turning F3 off
// disables both writes and reads).

package search

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	// eventInjectEnvVar is shared with the extractor (handlers package);
	// duplicated as a literal here to avoid a circular import.
	eventInjectEnvVar = "MEMDB_F3_EVENTS"

	// eventInjectTopN caps how many events we splice into a single search.
	// Five matches the cap on profile_mem snippets — enough to seed the
	// rerank pool without crowding out memory rows.
	eventInjectTopN = 5

	// eventInjectDateWindowDays is the half-window size around the query's
	// temporal cutoff. ±7 days catches "last week", "this past month" without
	// pulling unrelated events from earlier sessions.
	eventInjectDateWindowDays = 7

	// eventInjectScore is the seed score assigned to injected events.
	// Higher than RRF baseline (~0.016) so they survive top-K trim before
	// rerank, but still below highly-ranked vector hits — rerank takes over
	// from there.
	eventInjectScore = 0.05
)

// eventsPostgres is the subset of Postgres methods needed by stageInjectEvents.
// The concrete *db.Postgres satisfies it. Tests can supply a stub.
type eventsPostgres interface {
	SearchEventsByDate(ctx context.Context, cubeID, userID string, start, end time.Time, limit int) ([]db.EventEntry, error)
	SearchEventsByTag(ctx context.Context, cubeID, userID string, tags []string, limit int) ([]db.EventEntry, error)
	SearchEventsByCosine(ctx context.Context, cubeID, userID string, query []float32, k int) ([]db.EventEntry, error)
}

func eventInjectEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(eventInjectEnvVar)))
	switch v {
	case "false", "0":
		return false
	default:
		return true
	}
}

// --- metrics ---

const (
	eventSearchTypeDate   = "date"
	eventSearchTypeTag    = "tag"
	eventSearchTypeCosine = "cosine"
)

var (
	eventInjectMxOnce sync.Once
	eventInjectMx     struct {
		SearchDuration metric.Int64Histogram
		Injected       metric.Int64Counter
	}
)

func eventInjectMetrics() {
	eventInjectMxOnce.Do(func() {
		meter := otel.Meter("memdb-go/search")
		eventInjectMx.SearchDuration, _ = meter.Int64Histogram(
			"memdb.events.search_duration_ms",
			metric.WithDescription("F3 event lookup duration by type (date|tag|cosine)"),
			metric.WithExplicitBucketBoundaries(1, 5, 10, 25, 50, 100, 250, 500, 1000),
		)
		eventInjectMx.Injected, _ = meter.Int64Counter(
			"memdb.events.injected_total",
			metric.WithDescription("F3 events injected into candidate pool by selection type"),
		)
		// Pre-register every type at zero.
		ctx := context.Background()
		for _, tp := range []string{eventSearchTypeDate, eventSearchTypeTag, eventSearchTypeCosine} {
			eventInjectMx.SearchDuration.Record(ctx, 0, metric.WithAttributes(attribute.String("type", tp)))
			eventInjectMx.Injected.Add(ctx, 0, metric.WithAttributes(attribute.String("type", tp)))
		}
	})
}

func recordEventSearch(ctx context.Context, tp string, dur time.Duration, n int) {
	eventInjectMetrics()
	if eventInjectMx.SearchDuration != nil {
		eventInjectMx.SearchDuration.Record(ctx, dur.Milliseconds(),
			metric.WithAttributes(attribute.String("type", tp)))
	}
	if eventInjectMx.Injected != nil && n > 0 {
		eventInjectMx.Injected.Add(ctx, int64(n),
			metric.WithAttributes(attribute.String("type", tp)))
	}
}

// stageInjectEvents looks up F3 user_events relevant to the query and
// splices the matches into the text candidate pool BEFORE rerank.
//
// Soft-fail: returns nil on any DB error so the rest of the pipeline keeps
// running. Errors land in s.logger.Debug.
//
// Selection priority: date window (when cutoff present) → tag overlap (when
// SearchParams.Tags is set) → cosine (always, when embedding available).
// We stop at the first mode that returns ≥ 1 row to keep the per-search
// cost at exactly one DB query in the steady state.
func (s *SearchService) stageInjectEvents(ctx context.Context, st *pipelineState) error {
	if !eventInjectEnabled() {
		st.skip("inject_events")
		return nil
	}
	pg, ok := s.postgres.(eventsPostgres)
	if !ok || pg == nil {
		st.skip("inject_events")
		return nil
	}
	if st.Params.CubeID == "" || st.Params.UserName == "" {
		st.skip("inject_events")
		return nil
	}

	events, searchType := s.lookupEvents(ctx, pg, st)
	if len(events) == 0 {
		st.skip("inject_events")
		return nil
	}

	// Splice events as synthetic text candidates.
	injected := make([]MergedResult, 0, len(events))
	for _, ev := range events {
		injected = append(injected, eventToMergedResult(ev))
	}
	// Prepend so the score floor is preserved after the merge step's sort.
	st.TextMerged = append(injected, st.TextMerged...)

	if s.logger != nil {
		s.logger.Debug("inject_events spliced",
			slog.String("type", searchType),
			slog.Int("count", len(events)),
			slog.String("cube_id", st.Params.CubeID))
	}
	return nil
}

// lookupEvents picks the cheapest applicable selection mode and returns
// (events, type-label-for-metrics). Returns (nil, "") when nothing matched.
func (s *SearchService) lookupEvents(
	ctx context.Context, pg eventsPostgres, st *pipelineState,
) ([]db.EventEntry, string) {
	cubeID, userID := st.Params.CubeID, st.Params.UserName

	// 1. Date window — preferred when the query had a temporal cutoff.
	if st.HasCutoff && st.CutoffISO != "" {
		if cutoff, err := time.Parse("2006-01-02T15:04:05+00:00", st.CutoffISO); err == nil {
			start := cutoff.AddDate(0, 0, -eventInjectDateWindowDays)
			end := cutoff.AddDate(0, 0, eventInjectDateWindowDays)
			t0 := time.Now()
			events, err := pg.SearchEventsByDate(ctx, cubeID, userID, start, end, eventInjectTopN)
			recordEventSearch(ctx, eventSearchTypeDate, time.Since(t0), len(events))
			if err != nil {
				s.logger.Debug("inject_events: SearchEventsByDate failed", slog.Any("error", err))
			} else if len(events) > 0 {
				return events, eventSearchTypeDate
			}
		}
	}

	// 2. Tag overlap — when the caller threaded a Tags slice in.
	if len(st.Params.Tags) > 0 {
		t0 := time.Now()
		events, err := pg.SearchEventsByTag(ctx, cubeID, userID, st.Params.Tags, eventInjectTopN)
		recordEventSearch(ctx, eventSearchTypeTag, time.Since(t0), len(events))
		if err != nil {
			s.logger.Debug("inject_events: SearchEventsByTag failed", slog.Any("error", err))
		} else if len(events) > 0 {
			return events, eventSearchTypeTag
		}
	}

	// 3. Cosine — always available when the query was embedded.
	if len(st.QueryVec) > 0 {
		t0 := time.Now()
		events, err := pg.SearchEventsByCosine(ctx, cubeID, userID, st.QueryVec, eventInjectTopN)
		recordEventSearch(ctx, eventSearchTypeCosine, time.Since(t0), len(events))
		if err != nil {
			s.logger.Debug("inject_events: SearchEventsByCosine failed", slog.Any("error", err))
			return nil, ""
		}
		if len(events) > 0 {
			return events, eventSearchTypeCosine
		}
	}

	return nil, ""
}

// eventToMergedResult adapts a db.EventEntry into the candidate MergedResult
// shape so it flows through format_items + post_process unchanged. The
// properties JSON mirrors what FormatMemoryItem expects: id, memory,
// memory_type, plus the F3-specific event_date / tags fields so downstream
// rerank or LLM-judge stages can boost matching candidates.
func eventToMergedResult(ev db.EventEntry) MergedResult {
	props := map[string]any{
		"id":          ev.ID.String(),
		"memory":      ev.EventText,
		"memory_type": "EventSummary",
		"tags":        ev.Tags,
		"created_at":  ev.CreatedAt.Format(time.RFC3339),
	}
	if ev.EventDate != nil {
		props["event_date"] = ev.EventDate.Format("2006-01-02")
	}
	// Best-effort JSON encode — failure here means we drop the row's
	// metadata, but never abort the search. (json.Marshal on map[string]any
	// can only fail on cycles, which our shape cannot produce.)
	propsBytes, _ := json.Marshal(props)
	return MergedResult{
		ID:         ev.ID.String(),
		Properties: string(propsBytes),
		Score:      eventInjectScore,
	}
}
