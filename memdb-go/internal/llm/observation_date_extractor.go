// Package llm — observation_date_extractor.go: F11 observation_date coverage lift.
//
// Problem: 12.4% of DB entries have observation_date populated (1740/14002).
// Most callers do not pass chat_time on messages, so observationDateFromMessages
// returns "" — skipping the observation_date stamp entirely.
//
// Fix: when all messages lack chat_time, run a lightweight single-call LLM
// extractor over the conversation text to infer the primary conversation date.
// The extractor handles:
//   - Absolute dates ("On January 6, 2024")
//   - Relative anchors resolved against a reference date ("yesterday" → ref-1)
//   - Seasonal/approximate ("last summer" → ref-year-06-01 approximation)
//   - No-date conversations ("I love pizza") → returns "", no forced value
//
// The result is validated to YYYY-MM-DD before use. Non-strict inputs (relative
// dates) are resolved to ISO by the LLM using the reference timestamp. A metric
// memdb_extract_date_confidence_dropped_total{reason} fires when the extractor
// produces a non-ISO value that fails post-processing — matching the hard rule
// "NEVER silently disable".
package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ObsDateExtractor wraps a chat Client and extracts the primary conversation
// date from message text when no chat_time metadata is available.
// It is intentionally lightweight: one LLM call, 256 max tokens, 10s timeout.
type ObsDateExtractor struct {
	client *Client
}

// NewObsDateExtractor creates an ObsDateExtractor that reuses the supplied
// chat Client. The same client used by AtomicExtractor/LLMExtractor can be
// reused to avoid duplicate credential/model config.
func NewObsDateExtractor(c *Client) *ObsDateExtractor {
	return &ObsDateExtractor{client: c}
}

// obsDateResponse is the JSON envelope the LLM returns for date extraction.
type obsDateResponse struct {
	// Date is the extracted YYYY-MM-DD string, or "" when no date is inferable.
	Date string `json:"date"`
}

// obsDateSystem is the fixed system prompt for the date extractor.
const obsDateSystem = `You are a precise date extractor. Given a conversation and a reference date, extract the SINGLE most relevant calendar date the conversation took place on or refers to.

Rules:
1. Return ONLY a JSON object: {"date": "YYYY-MM-DD"} or {"date": ""}.
2. Resolve relative references ("yesterday", "last week", "next month") against the reference date to an exact YYYY-MM-DD.
3. For approximate anchors ("last summer", "early 2022", "a few months ago"), emit the nearest reasonable YYYY-MM-DD approximation (e.g. "last summer" with ref 2023-08-11 → "2023-06-01").
4. If the conversation contains NO temporal anchor at all (general preferences, timeless facts), return {"date": ""}.
5. NEVER return a placeholder like "YYYY-MM-DD". NEVER invent a date that isn't supported by the text.
6. When multiple dates appear, pick the one that best represents WHEN the conversation occurred (prefer the most recent concrete date mentioned).`

const (
	obsDateMaxTokens   = 256
	obsDateCallTimeout = 10 * time.Second
)

// obsDateDroppedMetricsOnce guards lazy init of the confidence-drop counter.
var (
	obsDateDroppedOnce sync.Once
	obsDateDroppedCtr  metric.Int64Counter
)

func obsDateDroppedCounter() metric.Int64Counter {
	obsDateDroppedOnce.Do(func() {
		m := otel.Meter("memdb-go/llm")
		obsDateDroppedCtr, _ = m.Int64Counter(
			"memdb_extract_date_confidence_dropped_total",
			metric.WithDescription("F11: observation_date candidates dropped after LLM extraction due to non-ISO format or empty response"),
		)
	})
	return obsDateDroppedCtr
}

// ExtractObservationDate attempts to infer the primary conversation date from
// the text of messages that lack chat_time.
//
// referenceDate is the caller's best available temporal anchor (e.g. today's
// date as "YYYY-MM-DD") used to resolve relative references. When empty,
// time.Now() UTC is used.
//
// Returns "YYYY-MM-DD" on success, "" when no date is inferable (this is
// valid — not all conversations have temporal anchors). Error is non-nil only
// on transport/LLM failure; the caller should treat it as a soft miss and
// continue without the date.
func (e *ObsDateExtractor) ExtractObservationDate(
	ctx context.Context,
	messages []string,
	referenceDate string,
) (string, error) {
	if e == nil || e.client == nil {
		return "", nil
	}
	if len(messages) == 0 {
		return "", nil
	}
	if referenceDate == "" {
		referenceDate = time.Now().UTC().Format("2006-01-02")
	}

	userContent := buildObsDateUserMessage(messages, referenceDate)
	msgs := []Message{
		{Role: "system", Content: obsDateSystem},
		{Role: "user", Content: userContent},
	}

	var resp obsDateResponse
	err := ChatStructured(ctx, e.client, "obs_date_extract", msgs, &resp,
		WithMaxTokens(obsDateMaxTokens),
		WithTimeout(obsDateCallTimeout),
		WithMaxRetries(0),
	)
	if err != nil {
		return "", fmt.Errorf("obs_date extract: %w", err)
	}

	d := strings.TrimSpace(resp.Date)
	if d == "" {
		// LLM correctly found no temporal anchor — not a failure.
		return "", nil
	}

	// Validate YYYY-MM-DD strictly.
	if _, parseErr := time.Parse("2006-01-02", d); parseErr != nil {
		obsDateDroppedCounter().Add(ctx, 1,
			metric.WithAttributes(attribute.String("reason", "non_iso_format")),
		)
		return "", nil
	}

	return d, nil
}

// buildObsDateUserMessage assembles the user-side prompt for date extraction.
func buildObsDateUserMessage(messages []string, referenceDate string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Reference date: %s\n\nConversation:\n", referenceDate)
	for i, m := range messages {
		fmt.Fprintf(&sb, "[%d] %s\n", i+1, m)
	}
	sb.WriteString("\nWhat YYYY-MM-DD date does this conversation take place on or primarily refer to?")
	return sb.String()
}
