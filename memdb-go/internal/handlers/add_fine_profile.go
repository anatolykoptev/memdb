package handlers

// add_fine_profile.go — fire-and-forget user-profile extraction hook
// (M10 Stream 2). Wired from nativeFineAddForCube after a successful
// ExtractAndDedup pass; runs in a bounded-concurrency goroutine so it
// never blocks the request path.
//
// Env gate: MEMDB_PROFILE_EXTRACT (default "true"; only "false"/"0" disable).
// Concurrency cap: profileExtractSemaphoreSize (8) — picked to match the
// existing add_fine fan-out budget on the prod 4-core box.
// Metrics:
//   - memdb.add.profile_extract_total{outcome=success|empty|llm_error|db_error|disabled|busy}
//   - memdb.add.profile_extract_duration_seconds
//
// On any error the goroutine logs at Debug and bumps the counter. Never
// returns an error to the caller.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/semaphore"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const (
	profileExtractEnvVar       = "MEMDB_PROFILE_EXTRACT"
	profileExtractSemaphoreSize = 8
	profileExtractTimeout      = 60 * time.Second
)

// profileExtractEnabled reports whether the post-add profile extraction
// goroutine should run. Default TRUE; only "false"/"0" disable.
func profileExtractEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(profileExtractEnvVar)))
	switch v {
	case "false", "0":
		return false
	default:
		return true
	}
}

// --- bounded concurrency ---

var (
	profileExtractSemOnce sync.Once
	profileExtractSem     *semaphore.Weighted
)

func profileExtractSemaphore() *semaphore.Weighted {
	profileExtractSemOnce.Do(func() {
		profileExtractSem = semaphore.NewWeighted(profileExtractSemaphoreSize)
	})
	return profileExtractSem
}

// --- metrics ---

const (
	profileOutcomeSuccess  = "success"
	profileOutcomeEmpty    = "empty"
	profileOutcomeLLMError = "llm_error"
	profileOutcomeDBError  = "db_error"
	profileOutcomeDisabled = "disabled"
	profileOutcomeBusy     = "busy" // semaphore acquire failed
)

var (
	profileMetricsOnce sync.Once
	profileMetrics     struct {
		Total    metric.Int64Counter
		Duration metric.Float64Histogram
	}
)

func profileExtractMetrics() {
	profileMetricsOnce.Do(func() {
		meter := otel.Meter("memdb-go/handlers")
		profileMetrics.Total, _ = meter.Int64Counter(
			"memdb.add.profile_extract_total",
			metric.WithDescription("User profile extraction outcomes"),
		)
		profileMetrics.Duration, _ = meter.Float64Histogram(
			"memdb.add.profile_extract_duration_seconds",
			metric.WithDescription("User profile extraction duration"),
			metric.WithUnit("s"),
		)
	})
}

func recordProfileExtractOutcome(ctx context.Context, outcome string, dur time.Duration) {
	profileExtractMetrics()
	if profileMetrics.Total != nil {
		profileMetrics.Total.Add(ctx, 1, metric.WithAttributes(
			attribute.String("outcome", outcome),
		))
	}
	if profileMetrics.Duration != nil {
		profileMetrics.Duration.Record(ctx, dur.Seconds())
	}
}

// --- entry point ---

// triggerProfileExtract launches a fire-and-forget profile extraction for
// the given user, scoped to a single cube. The conversation is captured by
// value into the goroutine so the caller can return immediately. Safe to
// call when the env gate is off (records "disabled" and returns).
//
// cubeID is required (security audit C1, migration 0017): every persisted
// row carries cube_id so that profile rows extracted in cube=A never leak
// into a chat scoped to cube=B. An empty cubeID short-circuits as
// "disabled" — same as missing user_id.
//
// Admission control (security audit C3): TryAcquire is called in the caller
// goroutine BEFORE spawning. If the semaphore is saturated the call returns
// false immediately — no goroutine is queued, no closure is allocated, the
// "busy" counter fires at the point of saturation (not 60 s later).
//
// Returns true when a goroutine was scheduled (useful for tests / metrics).
func (h *Handler) triggerProfileExtract(conversation, userID, cubeID string) bool {
	if h == nil || h.postgres == nil || h.llmExtractor == nil {
		// Required dependencies missing — silently skip. The fine-add path
		// itself would have been a proxy fallback in this state.
		return false
	}
	if !profileExtractEnabled() {
		recordProfileExtractOutcome(context.Background(), profileOutcomeDisabled, 0)
		return false
	}
	if userID == "" || cubeID == "" {
		// No user_id / cube_id → cannot persist with tenant isolation; treat
		// as disabled rather than error so the add path stays silent.
		recordProfileExtractOutcome(context.Background(), profileOutcomeDisabled, 0)
		return false
	}

	// ADMISSION CONTROL (C3): acquire semaphore slot BEFORE spawning.
	// Under burst load this bounds goroutine count to profileExtractSemaphoreSize.
	// Never queue — if all slots are occupied, drop the work immediately.
	sem := profileExtractSemaphore()
	if !sem.TryAcquire(1) {
		recordProfileExtractOutcome(context.Background(), profileOutcomeBusy, 0)
		h.logger.Debug("profile extract: semaphore saturated, dropping",
			slog.String("user_id", userID), slog.String("cube_id", cubeID))
		return false
	}
	// Semaphore slot is held; the goroutine is responsible for releasing it.
	go func() {
		defer sem.Release(1)
		h.runProfileExtractWithSem(conversation, userID, cubeID)
	}()
	return true
}

// runProfileExtractWithSem is the goroutine body. The caller has already
// acquired the semaphore; this function runs ExtractProfile (cube-scoped)
// and BulkUpserts the result under the per-call 60 s deadline.
func (h *Handler) runProfileExtractWithSem(conversation, userID, cubeID string) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), profileExtractTimeout)
	defer cancel()

	// Reuse the existing LLM client behind the fact extractor — same retry,
	// model fallback, and metrics namespace. Avoids duplicating credentials.
	pe := llm.NewProfileExtractor(h.llmExtractor.Client())

	entries, err := pe.ExtractProfile(ctx, conversation, userID, cubeID)
	if err != nil {
		if errors.Is(err, llm.ErrEmptyConversation) {
			recordProfileExtractOutcome(ctx, profileOutcomeEmpty, time.Since(start))
			return
		}
		recordProfileExtractOutcome(ctx, profileOutcomeLLMError, time.Since(start))
		h.logger.Debug("profile extract: LLM call failed",
			slog.String("user_id", userID), slog.String("cube_id", cubeID), slog.Any("error", err))
		return
	}
	if len(entries) == 0 {
		recordProfileExtractOutcome(ctx, profileOutcomeEmpty, time.Since(start))
		return
	}

	if err := h.postgres.BulkUpsert(ctx, entries); err != nil {
		recordProfileExtractOutcome(ctx, profileOutcomeDBError, time.Since(start))
		h.logger.Debug("profile extract: BulkUpsert failed",
			slog.String("user_id", userID),
			slog.String("cube_id", cubeID),
			slog.Int("entries", len(entries)),
			slog.Any("error", err))
		return
	}

	recordProfileExtractOutcome(ctx, profileOutcomeSuccess, time.Since(start))
	h.logger.Debug("profile extract: persisted",
		slog.String("user_id", userID),
		slog.String("cube_id", cubeID),
		slog.Int("entries", len(entries)))
}
