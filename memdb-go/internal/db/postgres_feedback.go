package db

// postgres_feedback.go — feedback_events + extract_examples table operations (M10 Stream 8).
//
// Tables: memos_graph.feedback_events, memos_graph.extract_examples
// Purpose: capture every feedback request for the M11 reward / RL loop.
// No background processing here — M11 closes the loop.

import (
	"context"
	"errors"
	"time"
)

// FeedbackEvent is a row from memos_graph.feedback_events.
type FeedbackEvent struct {
	ID         int64
	UserID     string
	CubeID     *string
	Query      string
	Prediction string
	Correction *string
	Label      string
	CreatedAt  time.Time
}

// InsertFeedbackEventParams is the input for a single InsertFeedbackEvent call.
type InsertFeedbackEventParams struct {
	UserID     string
	CubeID     *string // optional
	Query      string
	Prediction string
	Correction *string // optional — non-nil means label=="correction"
	Label      string  // positive | negative | neutral | correction
}

// ExtractExample is a row from memos_graph.extract_examples.
type ExtractExample struct {
	ID            int64
	PromptKind    string
	InputText     string
	GoldOutput    []byte // raw JSONB bytes
	SourceEventID *int64
	Active        bool
	CreatedAt     time.Time
}

// InsertExtractExampleParams is the input for a single InsertExtractExample call.
type InsertExtractExampleParams struct {
	PromptKind    string
	InputText     string
	GoldOutput    []byte // must be valid JSON
	SourceEventID *int64 // optional FK to feedback_events.id
}

const feedbackEventColumns = `id, user_id, cube_id, query, prediction, correction, label, created_at`

// InsertFeedbackEvent inserts a new feedback event row and returns the stored record.
func (p *Postgres) InsertFeedbackEvent(ctx context.Context, params InsertFeedbackEventParams) (FeedbackEvent, error) {
	if err := validateFeedbackEventParams(params); err != nil {
		return FeedbackEvent{}, err
	}

	row := p.pool.QueryRow(ctx, `
INSERT INTO memos_graph.feedback_events
    (user_id, cube_id, query, prediction, correction, label)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING `+feedbackEventColumns,
		params.UserID, params.CubeID,
		params.Query, params.Prediction,
		params.Correction, params.Label,
	)
	return scanFeedbackEventRow(row)
}

// InsertExtractExample inserts a curated gold example row.
// gold_output must be valid JSON — the caller is responsible for marshalling.
func (p *Postgres) InsertExtractExample(ctx context.Context, params InsertExtractExampleParams) (ExtractExample, error) {
	if params.PromptKind == "" {
		return ExtractExample{}, errors.New("InsertExtractExample: prompt_kind required")
	}
	if len(params.GoldOutput) == 0 {
		return ExtractExample{}, errors.New("InsertExtractExample: gold_output required")
	}

	var ex ExtractExample
	err := p.pool.QueryRow(ctx, `
INSERT INTO memos_graph.extract_examples
    (prompt_kind, input_text, gold_output, source_event_id)
VALUES ($1, $2, $3, $4)
RETURNING id, prompt_kind, input_text, gold_output, source_event_id, active, created_at`,
		params.PromptKind, params.InputText,
		params.GoldOutput, params.SourceEventID,
	).Scan(
		&ex.ID, &ex.PromptKind, &ex.InputText, &ex.GoldOutput,
		&ex.SourceEventID, &ex.Active, &ex.CreatedAt,
	)
	if err != nil {
		return ExtractExample{}, err
	}
	return ex, nil
}

// GetFeedbackEventsByUser returns all feedback events for a user, newest first.
func (p *Postgres) GetFeedbackEventsByUser(ctx context.Context, userID string) ([]FeedbackEvent, error) {
	if userID == "" {
		return nil, errors.New("GetFeedbackEventsByUser: user_id required")
	}

	rows, err := p.pool.Query(ctx, `
SELECT `+feedbackEventColumns+`
FROM memos_graph.feedback_events
WHERE user_id = $1
ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FeedbackEvent
	for rows.Next() {
		ev, scanErr := scanFeedbackEventRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// --- internal helpers ---

func scanFeedbackEventRow(scanner interface {
	Scan(dest ...any) error
}) (FeedbackEvent, error) {
	var ev FeedbackEvent
	err := scanner.Scan(
		&ev.ID, &ev.UserID, &ev.CubeID,
		&ev.Query, &ev.Prediction, &ev.Correction,
		&ev.Label, &ev.CreatedAt,
	)
	return ev, err
}

func validateFeedbackEventParams(p InsertFeedbackEventParams) error {
	if p.UserID == "" {
		return errors.New("InsertFeedbackEvent: user_id required")
	}
	if p.Query == "" {
		return errors.New("InsertFeedbackEvent: query required")
	}
	if p.Prediction == "" {
		return errors.New("InsertFeedbackEvent: prediction required")
	}
	switch p.Label {
	case "positive", "negative", "neutral", "correction":
	default:
		return errors.New("InsertFeedbackEvent: label must be positive|negative|neutral|correction")
	}
	return nil
}
