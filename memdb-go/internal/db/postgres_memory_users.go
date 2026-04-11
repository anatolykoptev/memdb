package db

// postgres_memory_users.go — user and cube identity queries against the Memory graph.
// Covers: list users (cube slot + person slot), count, existence check, cube-by-tag.

import (
	"context"
	"fmt"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db/queries"
)

// UserIdentity is a person-level identity derived from Memory rows (user_id slot).
type UserIdentity struct {
	UserID    string
	FirstSeen time.Time
}

// ListUsers returns distinct user_name values (cube slot) from activated memories.
func (p *Postgres) ListUsers(ctx context.Context) ([]string, error) {
	q := fmt.Sprintf(queries.ListUsers, graphName)
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		users = append(users, name)
	}
	return users, rows.Err()
}

// ListDistinctUserIDs returns distinct person identities (user_id slot) with first-seen time.
// Phase 2: uses properties->>'user_id' (person slot), not user_name (cube slot).
func (p *Postgres) ListDistinctUserIDs(ctx context.Context) ([]UserIdentity, error) {
	q := fmt.Sprintf(queries.ListDistinctUserIDs, graphName)
	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []UserIdentity
	for rows.Next() {
		var u UserIdentity
		if err := rows.Scan(&u.UserID, &u.FirstSeen); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CountDistinctUsers returns the number of distinct users with activated memories.
func (p *Postgres) CountDistinctUsers(ctx context.Context) (int, error) {
	q := fmt.Sprintf(queries.CountDistinctUsers, graphName)
	var count int
	err := p.pool.QueryRow(ctx, q).Scan(&count)
	return count, err
}

// ExistUser checks whether a user has any activated memories.
func (p *Postgres) ExistUser(ctx context.Context, userName string) (bool, error) {
	q := fmt.Sprintf(queries.ExistUser, graphName)
	var exists bool
	err := p.pool.QueryRow(ctx, q, userName).Scan(&exists)
	return exists, err
}

// ListCubesByTag returns distinct cube IDs whose activated memories include
// the given tag in their properties->'tags' array.
func (p *Postgres) ListCubesByTag(ctx context.Context, tag string) ([]string, error) {
	q := fmt.Sprintf(queries.ListCubesByTag, graphName)
	rows, err := p.pool.Query(ctx, q, tag)
	if err != nil {
		return nil, fmt.Errorf("list cubes by tag: %w", err)
	}
	defer rows.Close()
	var cubes []string
	for rows.Next() {
		var cube string
		if err := rows.Scan(&cube); err != nil {
			return nil, err
		}
		cubes = append(cubes, cube)
	}
	return cubes, rows.Err()
}
