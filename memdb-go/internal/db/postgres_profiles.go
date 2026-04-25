package db

// postgres_profiles.go — user_profiles table operations (M10 Stream 1).
//
// Table: memos_graph.user_profiles
// Soft-delete: expired_at IS NULL → active row; set expired_at = NOW() to delete.
// BulkUpsert: single-transaction batch; conflict resolution per row.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProfileEntry is a row from memos_graph.user_profiles.
type ProfileEntry struct {
	ID         int64
	UserID     string
	Topic      string
	SubTopic   string
	Memo       string
	Confidence float32
	ValidAt    *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ExpiredAt  *time.Time
}

// InsertProfileParams is the input for a single InsertProfile call.
type InsertProfileParams struct {
	UserID     string
	Topic      string
	SubTopic   string
	Memo       string
	Confidence float32    // 0 → server default (1.0)
	ValidAt    *time.Time // optional
}

// UpdateProfileParams updates memo/confidence/valid_at on an existing active row.
type UpdateProfileParams struct {
	UserID     string
	Topic      string
	SubTopic   string
	Memo       string
	Confidence float32
	ValidAt    *time.Time
}

// ErrProfileNotFound is returned when no active row matches the given key.
var ErrProfileNotFound = errors.New("user profile not found")

const profileColumns = `id, user_id, topic, sub_topic, memo, confidence,
	valid_at, created_at, updated_at, expired_at`

// InsertProfile inserts a new active profile row.
// Returns a unique-constraint error if an active row already exists for
// (user_id, topic, sub_topic). Use BulkUpsert for upsert semantics.
func (p *Postgres) InsertProfile(ctx context.Context, params InsertProfileParams) (ProfileEntry, error) {
	if err := validateProfileKey(params.UserID, params.Topic, params.SubTopic); err != nil {
		return ProfileEntry{}, fmt.Errorf("InsertProfile: %w", err)
	}
	if params.Confidence == 0 {
		params.Confidence = 1.0
	}

	row := p.pool.QueryRow(ctx, `
INSERT INTO memos_graph.user_profiles
    (user_id, topic, sub_topic, memo, confidence, valid_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING `+profileColumns,
		params.UserID, params.Topic, params.SubTopic,
		params.Memo, params.Confidence, params.ValidAt,
	)
	return scanProfileRow(row)
}

// UpdateProfile updates memo, confidence, valid_at, and updated_at on the
// single active row for (user_id, topic, sub_topic).
// Returns ErrProfileNotFound when no active row matches.
func (p *Postgres) UpdateProfile(ctx context.Context, params UpdateProfileParams) (ProfileEntry, error) {
	if err := validateProfileKey(params.UserID, params.Topic, params.SubTopic); err != nil {
		return ProfileEntry{}, fmt.Errorf("UpdateProfile: %w", err)
	}

	row := p.pool.QueryRow(ctx, `
UPDATE memos_graph.user_profiles
SET memo       = $4,
    confidence = $5,
    valid_at   = $6,
    updated_at = NOW()
WHERE user_id   = $1
  AND topic     = $2
  AND sub_topic = $3
  AND expired_at IS NULL
RETURNING `+profileColumns,
		params.UserID, params.Topic, params.SubTopic,
		params.Memo, params.Confidence, params.ValidAt,
	)
	entry, err := scanProfileRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfileEntry{}, ErrProfileNotFound
	}
	return entry, err
}

// GetProfilesByUser returns all active profile entries for a user, ordered by
// topic, sub_topic, updated_at DESC.
func (p *Postgres) GetProfilesByUser(ctx context.Context, userID string) ([]ProfileEntry, error) {
	if userID == "" {
		return nil, errors.New("GetProfilesByUser: user_id required")
	}

	rows, err := p.pool.Query(ctx, `
SELECT `+profileColumns+`
FROM memos_graph.user_profiles
WHERE user_id = $1
  AND expired_at IS NULL
ORDER BY topic, sub_topic, updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("GetProfilesByUser query: %w", err)
	}
	defer rows.Close()
	return collectProfileRows(rows)
}

// GetProfilesByTopic returns all active profile entries for a user+topic,
// ordered by sub_topic, updated_at DESC.
func (p *Postgres) GetProfilesByTopic(ctx context.Context, userID, topic string) ([]ProfileEntry, error) {
	if userID == "" || topic == "" {
		return nil, errors.New("GetProfilesByTopic: user_id and topic required")
	}

	rows, err := p.pool.Query(ctx, `
SELECT `+profileColumns+`
FROM memos_graph.user_profiles
WHERE user_id  = $1
  AND topic    = $2
  AND expired_at IS NULL
ORDER BY sub_topic, updated_at DESC`,
		userID, topic,
	)
	if err != nil {
		return nil, fmt.Errorf("GetProfilesByTopic query: %w", err)
	}
	defer rows.Close()
	return collectProfileRows(rows)
}

// SoftDeleteProfile sets expired_at = NOW() on the active row for
// (user_id, topic, sub_topic). Returns ErrProfileNotFound when not found.
func (p *Postgres) SoftDeleteProfile(ctx context.Context, userID, topic, subTopic string) error {
	if err := validateProfileKey(userID, topic, subTopic); err != nil {
		return fmt.Errorf("SoftDeleteProfile: %w", err)
	}

	tag, err := p.pool.Exec(ctx, `
UPDATE memos_graph.user_profiles
SET expired_at = NOW(), updated_at = NOW()
WHERE user_id   = $1
  AND topic     = $2
  AND sub_topic = $3
  AND expired_at IS NULL`,
		userID, topic, subTopic,
	)
	if err != nil {
		return fmt.Errorf("SoftDeleteProfile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProfileNotFound
	}
	return nil
}

// BulkUpsert inserts or updates a slice of profile entries in a single
// transaction. Conflict resolution per row:
//   - No active row exists → INSERT.
//   - Active row exists with identical memo → no-op (DO NOTHING).
//   - Active row exists with different memo → expire old row, INSERT new row.
func (p *Postgres) BulkUpsert(ctx context.Context, entries []InsertProfileParams) error {
	if len(entries) == 0 {
		return nil
	}

	// Validate all entries before touching the pool.
	for i, e := range entries {
		if err := validateProfileKey(e.UserID, e.Topic, e.SubTopic); err != nil {
			return fmt.Errorf("BulkUpsert entry[%d]: %w", i, err)
		}
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("BulkUpsert begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for i, e := range entries {
		if e.Confidence == 0 {
			e.Confidence = 1.0
		}

		// Expire existing active row only when memo differs.
		if _, err := tx.Exec(ctx, `
UPDATE memos_graph.user_profiles
SET expired_at = NOW(), updated_at = NOW()
WHERE user_id   = $1
  AND topic     = $2
  AND sub_topic = $3
  AND expired_at IS NULL
  AND memo != $4`,
			e.UserID, e.Topic, e.SubTopic, e.Memo,
		); err != nil {
			return fmt.Errorf("BulkUpsert expire entry[%d]: %w", i, err)
		}

		// Insert new row; skip if an active row with the same memo already exists.
		if _, err = tx.Exec(ctx, `
INSERT INTO memos_graph.user_profiles
    (user_id, topic, sub_topic, memo, confidence, valid_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, topic, sub_topic) WHERE expired_at IS NULL
DO NOTHING`,
			e.UserID, e.Topic, e.SubTopic, e.Memo, e.Confidence, e.ValidAt,
		); err != nil {
			return fmt.Errorf("BulkUpsert insert entry[%d]: %w", i, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("BulkUpsert commit: %w", err)
	}
	return nil
}

// --- internal helpers ---

func scanProfileRow(row pgx.Row) (ProfileEntry, error) {
	var e ProfileEntry
	err := row.Scan(
		&e.ID, &e.UserID, &e.Topic, &e.SubTopic, &e.Memo, &e.Confidence,
		&e.ValidAt, &e.CreatedAt, &e.UpdatedAt, &e.ExpiredAt,
	)
	if err != nil {
		return ProfileEntry{}, err
	}
	return e, nil
}

func collectProfileRows(rows pgx.Rows) ([]ProfileEntry, error) {
	var out []ProfileEntry
	for rows.Next() {
		var e ProfileEntry
		if err := rows.Scan(
			&e.ID, &e.UserID, &e.Topic, &e.SubTopic, &e.Memo, &e.Confidence,
			&e.ValidAt, &e.CreatedAt, &e.UpdatedAt, &e.ExpiredAt,
		); err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func validateProfileKey(userID, topic, subTopic string) error {
	if userID == "" {
		return errors.New("user_id required")
	}
	if topic == "" {
		return errors.New("topic required")
	}
	if subTopic == "" {
		return errors.New("sub_topic required")
	}
	return nil
}
