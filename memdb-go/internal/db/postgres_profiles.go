package db

// postgres_profiles.go — user_profiles table operations (M10 Stream 1).
//
// Table: memos_graph.user_profiles
// Soft-delete: expired_at IS NULL → active row; set expired_at = NOW() to delete.
// BulkUpsert: single-transaction batch; conflict resolution per row.
//
// Cube isolation (security audit C1, migration 0017):
//   Every row carries a cube_id. The unique index is scoped by
//   (cube_id, user_id, topic, sub_topic) WHERE expired_at IS NULL, so two
//   cubes can independently host the same (topic, sub_topic) tuple for the
//   same user without colliding. Legacy rows from before migration 0017 may
//   still have cube_id = NULL; the cube-scoped getter
//   (GetProfilesByUserCube) excludes them — they are treated as
//   "pre-cube / global" and must not leak into a tenant chat.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProfileEntry is a row from memos_graph.user_profiles.
type ProfileEntry struct {
	ID         int64
	UserID     string
	CubeID     string // empty when the row was inserted before migration 0017
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
	CubeID     string // required (security audit C1); enforced by validateProfileKey
	Topic      string
	SubTopic   string
	Memo       string
	Confidence float32    // 0 → server default (1.0)
	ValidAt    *time.Time // optional
}

// UpdateProfileParams updates memo/confidence/valid_at on an existing active row.
type UpdateProfileParams struct {
	UserID     string
	CubeID     string // required (security audit C1)
	Topic      string
	SubTopic   string
	Memo       string
	Confidence float32
	ValidAt    *time.Time
}

// ErrProfileNotFound is returned when no active row matches the given key.
var ErrProfileNotFound = errors.New("user profile not found")

const profileColumns = `id, user_id, cube_id, topic, sub_topic, memo, confidence,
	valid_at, created_at, updated_at, expired_at`

// InsertProfile inserts a new active profile row.
// Returns a unique-constraint error if an active row already exists for
// (cube_id, user_id, topic, sub_topic). Use BulkUpsert for upsert semantics.
func (p *Postgres) InsertProfile(ctx context.Context, params InsertProfileParams) (ProfileEntry, error) {
	if err := validateProfileKey(params.UserID, params.CubeID, params.Topic, params.SubTopic); err != nil {
		return ProfileEntry{}, fmt.Errorf("InsertProfile: %w", err)
	}
	if params.Confidence == 0 {
		params.Confidence = 1.0
	}

	row := p.pool.QueryRow(ctx, `
INSERT INTO memos_graph.user_profiles
    (user_id, cube_id, topic, sub_topic, memo, confidence, valid_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING `+profileColumns,
		params.UserID, params.CubeID, params.Topic, params.SubTopic,
		params.Memo, params.Confidence, params.ValidAt,
	)
	return scanProfileRow(row)
}

// UpdateProfile updates memo, confidence, valid_at, and updated_at on the
// single active row for (cube_id, user_id, topic, sub_topic).
// Returns ErrProfileNotFound when no active row matches.
// Confidence == 0 is promoted to 1.0, consistent with InsertProfile behaviour.
func (p *Postgres) UpdateProfile(ctx context.Context, params UpdateProfileParams) (ProfileEntry, error) {
	if err := validateProfileKey(params.UserID, params.CubeID, params.Topic, params.SubTopic); err != nil {
		return ProfileEntry{}, fmt.Errorf("UpdateProfile: %w", err)
	}
	if params.Confidence == 0 {
		params.Confidence = 1.0
	}

	row := p.pool.QueryRow(ctx, `
UPDATE memos_graph.user_profiles
SET memo       = $5,
    confidence = $6,
    valid_at   = $7,
    updated_at = NOW()
WHERE user_id   = $1
  AND cube_id   = $2
  AND topic     = $3
  AND sub_topic = $4
  AND expired_at IS NULL
RETURNING `+profileColumns,
		params.UserID, params.CubeID, params.Topic, params.SubTopic,
		params.Memo, params.Confidence, params.ValidAt,
	)
	entry, err := scanProfileRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfileEntry{}, ErrProfileNotFound
	}
	return entry, err
}

// deprecatedGetProfilesByUserOnce ensures we only emit the deprecation warning
// once per process so admin/migration tools that loop over many users do not
// flood the log.
var deprecatedGetProfilesByUserOnce sync.Once

// GetProfilesByUser returns all active profile entries for a user across ALL
// cubes, ordered by topic, sub_topic, updated_at DESC.
//
// Deprecated: this method ignores cube isolation and is retained only for
// admin / migration tools. Production request handlers (chat, etc.) MUST use
// GetProfilesByUserCube to avoid cross-tenant profile leakage (security audit
// finding C1, migration 0017). A one-shot deprecation warning is logged on
// first use per process.
func (p *Postgres) GetProfilesByUser(ctx context.Context, userID string) ([]ProfileEntry, error) {
	if userID == "" {
		return nil, errors.New("GetProfilesByUser: user_id required")
	}
	deprecatedGetProfilesByUserOnce.Do(func() {
		slog.Warn("db.GetProfilesByUser is deprecated; use GetProfilesByUserCube to enforce cube isolation (security audit C1)")
	})

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

// GetProfilesByUserCube returns all active profile entries for a user scoped
// to a single cube, ordered by topic, sub_topic, updated_at DESC.
//
// Rows with NULL cube_id (legacy data inserted before migration 0017) are
// excluded — they are treated as "pre-cube / global" and must never leak into
// a tenant chat. See security audit finding C1.
//
// Both userID and cubeID are required; an empty string for either returns an
// error rather than silently widening the scan.
func (p *Postgres) GetProfilesByUserCube(ctx context.Context, userID, cubeID string) ([]ProfileEntry, error) {
	if userID == "" {
		return nil, errors.New("GetProfilesByUserCube: user_id required")
	}
	if cubeID == "" {
		return nil, errors.New("GetProfilesByUserCube: cube_id required")
	}

	rows, err := p.pool.Query(ctx, `
SELECT `+profileColumns+`
FROM memos_graph.user_profiles
WHERE user_id = $1
  AND cube_id = $2
  AND expired_at IS NULL
ORDER BY topic, sub_topic, updated_at DESC`,
		userID, cubeID,
	)
	if err != nil {
		return nil, fmt.Errorf("GetProfilesByUserCube query: %w", err)
	}
	defer rows.Close()
	return collectProfileRows(rows)
}

// GetProfilesByTopic returns all active profile entries for a user+topic
// across ALL cubes, ordered by sub_topic, updated_at DESC.
//
// Note: this helper is currently used only by admin / debug tooling. The
// chat path uses GetProfilesByUserCube. If a future caller needs per-cube
// topic scans, add GetProfilesByTopicCube alongside this method rather than
// changing the signature here.
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
// (cube_id, user_id, topic, sub_topic). Returns ErrProfileNotFound when not found.
func (p *Postgres) SoftDeleteProfile(ctx context.Context, userID, cubeID, topic, subTopic string) error {
	if err := validateProfileKey(userID, cubeID, topic, subTopic); err != nil {
		return fmt.Errorf("SoftDeleteProfile: %w", err)
	}

	tag, err := p.pool.Exec(ctx, `
UPDATE memos_graph.user_profiles
SET expired_at = NOW(), updated_at = NOW()
WHERE user_id   = $1
  AND cube_id   = $2
  AND topic     = $3
  AND sub_topic = $4
  AND expired_at IS NULL`,
		userID, cubeID, topic, subTopic,
	)
	if err != nil {
		return fmt.Errorf("SoftDeleteProfile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProfileNotFound
	}
	return nil
}

// profileDedupKey is the composite key used to deduplicate BulkUpsert entries.
// Includes cube_id so two cubes can independently carry the same (topic,
// sub_topic) tuple for the same user without one stomping the other.
type profileDedupKey struct{ cubeID, topic, subTopic string }

// dedupProfileEntries collapses duplicate (cube_id, topic, sub_topic) entries
// within a batch, keeping the last occurrence per key (last-wins semantics).
// Order of surviving entries matches their first appearance in the input slice.
func dedupProfileEntries(entries []InsertProfileParams) []InsertProfileParams {
	seen := make(map[profileDedupKey]int, len(entries))
	out := make([]InsertProfileParams, 0, len(entries))
	for _, e := range entries {
		k := profileDedupKey{e.CubeID, e.Topic, e.SubTopic}
		if idx, ok := seen[k]; ok {
			out[idx] = e // overwrite with later entry
		} else {
			seen[k] = len(out)
			out = append(out, e)
		}
	}
	return out
}

// BulkUpsert inserts or updates a slice of profile entries in a single
// transaction. Conflict resolution per row:
//   - No active row exists → INSERT.
//   - Active row exists with identical memo → no-op (DO NOTHING).
//   - Active row exists with different memo → expire old row, INSERT new row.
//
// Conflict scope is (cube_id, user_id, topic, sub_topic). Duplicate
// (cube_id, topic, sub_topic) pairs within a single batch are deduplicated
// before the transaction: the last occurrence per key wins.
func (p *Postgres) BulkUpsert(ctx context.Context, entries []InsertProfileParams) error {
	if len(entries) == 0 {
		return nil
	}

	// Validate all entries before touching the pool.
	for i, e := range entries {
		if err := validateProfileKey(e.UserID, e.CubeID, e.Topic, e.SubTopic); err != nil {
			return fmt.Errorf("BulkUpsert entry[%d]: %w", i, err)
		}
	}

	entries = dedupProfileEntries(entries)

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
  AND cube_id   = $2
  AND topic     = $3
  AND sub_topic = $4
  AND expired_at IS NULL
  AND memo != $5`,
			e.UserID, e.CubeID, e.Topic, e.SubTopic, e.Memo,
		); err != nil {
			return fmt.Errorf("BulkUpsert expire entry[%d]: %w", i, err)
		}

		// Insert new row; skip if an active row with the same memo already exists.
		if _, err = tx.Exec(ctx, `
INSERT INTO memos_graph.user_profiles
    (user_id, cube_id, topic, sub_topic, memo, confidence, valid_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (cube_id, user_id, topic, sub_topic) WHERE expired_at IS NULL
DO NOTHING`,
			e.UserID, e.CubeID, e.Topic, e.SubTopic, e.Memo, e.Confidence, e.ValidAt,
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
	var cubeID *string
	err := row.Scan(
		&e.ID, &e.UserID, &cubeID, &e.Topic, &e.SubTopic, &e.Memo, &e.Confidence,
		&e.ValidAt, &e.CreatedAt, &e.UpdatedAt, &e.ExpiredAt,
	)
	if err != nil {
		return ProfileEntry{}, err
	}
	if cubeID != nil {
		e.CubeID = *cubeID
	}
	return e, nil
}

func collectProfileRows(rows pgx.Rows) ([]ProfileEntry, error) {
	var out []ProfileEntry
	for rows.Next() {
		var e ProfileEntry
		var cubeID *string
		if err := rows.Scan(
			&e.ID, &e.UserID, &cubeID, &e.Topic, &e.SubTopic, &e.Memo, &e.Confidence,
			&e.ValidAt, &e.CreatedAt, &e.UpdatedAt, &e.ExpiredAt,
		); err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		if cubeID != nil {
			e.CubeID = *cubeID
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func validateProfileKey(userID, cubeID, topic, subTopic string) error {
	if userID == "" {
		return errors.New("user_id required")
	}
	if cubeID == "" {
		return errors.New("cube_id required")
	}
	if topic == "" {
		return errors.New("topic required")
	}
	if subTopic == "" {
		return errors.New("sub_topic required")
	}
	return nil
}
