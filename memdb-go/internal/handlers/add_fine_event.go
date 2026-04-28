package handlers

// add_fine_event.go — fire-and-forget Memobase event extraction (M11 F3).
//
// Mirrors triggerProfileExtract: bounded-concurrency goroutine that runs
// after a successful fine-add. Persists rows into memos_graph.user_events
// for the F3 search-time inject stage to consume.
//
// Env gate: MEMDB_F3_EVENTS (default "true"; only "false"/"0" disable).
// Concurrency cap: eventExtractSemaphoreSize (4) — half the profile budget
// because event extraction runs alongside profile extraction (parallel
// per-/add fan-out, so total pressure stays bounded).
//
// Metrics:
//   - memdb.events.extracted_total{outcome=success|empty|llm_error|db_error|disabled|busy}
//   - memdb.events.extract_duration_seconds
//
// On any error the goroutine logs at Debug. The sync write path is untouched.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/semaphore"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/featureflags"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	eventExtractSemaphoreSize = 4
	eventExtractTimeout       = 60 * time.Second
)

// eventExtractEnabled reports whether the post-add event extraction goroutine
// should run. Default TRUE; only "false"/"0" disable. Shares MEMDB_F3_EVENTS
// with the search-time injector via internal/featureflags.
func eventExtractEnabled() bool {
	return featureflags.F3EventsEnabled()
}

// --- bounded concurrency ---

var (
	eventExtractSemOnce sync.Once
	eventExtractSem     *semaphore.Weighted
)

func eventExtractSemaphore() *semaphore.Weighted {
	eventExtractSemOnce.Do(func() {
		eventExtractSem = semaphore.NewWeighted(eventExtractSemaphoreSize)
	})
	return eventExtractSem
}

// --- metrics ---

const (
	eventOutcomeSuccess  = "success"
	eventOutcomeEmpty    = "empty"
	eventOutcomeLLMError = "llm_error"
	eventOutcomeDBError  = "db_error"
	eventOutcomeDisabled = "disabled"
	eventOutcomeBusy     = "busy"
)

var (
	eventMetricsOnce sync.Once
	eventMetrics     struct {
		Total    metric.Int64Counter
		Duration metric.Float64Histogram
	}
)

func eventExtractMetrics() {
	eventMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/handlers")
		eventMetrics.Total, _ = meter.Int64Counter(
			"memdb.events.extracted_total",
			metric.WithDescription("F3 event extraction outcomes"),
		)
		eventMetrics.Duration, _ = meter.Float64Histogram(
			"memdb.events.extract_duration_seconds",
			metric.WithDescription("F3 event extraction duration"),
			metric.WithUnit("s"),
		)
		// Pre-register every outcome at zero so dashboards see all series from start.
		ctx := context.Background()
		for _, oc := range []string{
			eventOutcomeSuccess, eventOutcomeEmpty, eventOutcomeLLMError,
			eventOutcomeDBError, eventOutcomeDisabled, eventOutcomeBusy,
		} {
			eventMetrics.Total.Add(ctx, 0, metric.WithAttributes(attribute.String("outcome", oc)))
		}
	})
}

func recordEventOutcome(ctx context.Context, outcome string, dur time.Duration) {
	eventExtractMetrics()
	if eventMetrics.Total != nil {
		eventMetrics.Total.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}
	if eventMetrics.Duration != nil {
		eventMetrics.Duration.Record(ctx, dur.Seconds())
	}
}

// --- entry point ---

// triggerEventExtract launches a fire-and-forget event extraction. Mirrors
// triggerProfileExtract — both run from triggerBackgroundExtractors and share
// the per-/add conversation/userID/cubeID context.
//
// `now` is the request-time anchor (already computed by the caller); it
// flows into llm.EventExtractor.Extract so date-relative event extraction
// uses the same instant the rest of the /add pipeline sees.  An empty or
// unparseable now collapses to time.Now().UTC() — never breaks the call.
//
// Returns true when a goroutine was scheduled (used by tests / callers that
// want to know whether the work was admitted).
func (h *Handler) triggerEventExtract(conversation, userID, cubeID, now string) bool {
	if h == nil || h.postgres == nil || h.llmExtractor == nil {
		return false
	}
	if !eventExtractEnabled() {
		recordEventOutcome(context.Background(), eventOutcomeDisabled, 0)
		return false
	}
	if userID == "" || cubeID == "" {
		recordEventOutcome(context.Background(), eventOutcomeDisabled, 0)
		return false
	}
	sem := eventExtractSemaphore()
	if !sem.TryAcquire(1) {
		recordEventOutcome(context.Background(), eventOutcomeBusy, 0)
		h.logger.Debug("event extract: semaphore saturated, dropping",
			slog.String("user_id", userID), slog.String("cube_id", cubeID))
		return false
	}
	go func() {
		defer sem.Release(1)
		h.runEventExtractWithSem(conversation, userID, cubeID, now)
	}()
	return true
}

// parseNowAnchor parses the ISO timestamp produced by nowTimestamp() and falls
// back to time.Now().UTC() on any error so a malformed anchor never breaks
// extraction.
func parseNowAnchor(now string) time.Time {
	if now == "" {
		return time.Now().UTC()
	}
	const layout = "2006-01-02T15:04:05.000000"
	if t, err := time.Parse(layout, now); err == nil {
		return t.UTC()
	}
	const layout2 = "2006-01-02T15:04:05"
	if t, err := time.Parse(layout2, now); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}

// runEventExtractWithSem is the goroutine body. The caller has already
// acquired the semaphore; this function runs Extract under a 60s deadline,
// embeds each event text, and inserts the rows into user_events.
func (h *Handler) runEventExtractWithSem(conversation, userID, cubeID, now string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), eventExtractTimeout)
	defer cancel()

	ee := llm.NewEventExtractor(h.llmExtractor.Client())
	events, err := ee.Extract(ctx, conversation, parseNowAnchor(now))
	if err != nil {
		if errors.Is(err, llm.ErrEventEmptyConversation) {
			recordEventOutcome(ctx, eventOutcomeEmpty, time.Since(start))
			return
		}
		recordEventOutcome(ctx, eventOutcomeLLMError, time.Since(start))
		h.logger.Debug("event extract: LLM call failed",
			slog.String("user_id", userID), slog.String("cube_id", cubeID), slog.Any("error", err))
		return
	}
	if len(events) == 0 {
		recordEventOutcome(ctx, eventOutcomeEmpty, time.Since(start))
		return
	}

	// Embed event_text for cosine search. Failures are non-fatal — the row
	// can still be inserted without an embedding (date/tag search still works).
	// M2 followup: skip the texts/embed allocations entirely when len==0
	// (the early `if len(events) == 0` above already handles that, so the
	// allocation here is by definition non-empty — kept as defence-in-depth
	// in case future code paths reach this point with zero events).
	var embeddings [][]float32
	if h.embedder != nil && len(events) > 0 {
		texts := make([]string, len(events))
		for i, ev := range events {
			texts[i] = ev.EventText
		}
		if vecs, embErr := h.embedder.Embed(ctx, texts); embErr == nil {
			embeddings = vecs
		} else {
			h.logger.Debug("event extract: embed failed (continuing without embeddings)",
				slog.String("cube_id", cubeID), slog.Any("error", embErr))
		}
	}

	inserted := 0
	for i, ev := range events {
		params := db.InsertEventParams{
			CubeID:    cubeID,
			UserID:    userID,
			EventText: ev.EventText,
			EventDate: ev.EventDate,
			Tags:      ev.Tags,
		}
		if i < len(embeddings) {
			params.Embedding = embeddings[i]
		}
		if _, err := h.postgres.InsertEvent(ctx, params); err != nil {
			recordEventOutcome(ctx, eventOutcomeDBError, time.Since(start))
			h.logger.Debug("event extract: insert failed",
				slog.String("user_id", userID), slog.String("cube_id", cubeID),
				slog.Any("error", err))
			return
		}
		inserted++
	}

	recordEventOutcome(ctx, eventOutcomeSuccess, time.Since(start))
	h.logger.Debug("event extract: persisted",
		slog.String("user_id", userID),
		slog.String("cube_id", cubeID),
		slog.Int("events", inserted))
}
