package db

// postgres_config.go — user_configs table operations.
// Covers: EnsureUserConfigsTable, GetUserConfig, UpdateUserConfig.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MemDBai/MemDB/memdb-go/internal/db/queries"
)

// EnsureUserConfigsTable creates the user_configs table if it does not exist.
func (p *Postgres) EnsureUserConfigsTable(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, queries.CreateUserConfigsTable); err != nil {
		return fmt.Errorf("ensure user_configs table: %w", err)
	}
	return nil
}

// GetUserConfig retrieves the user config JSONB, returning nil if not found.
func (p *Postgres) GetUserConfig(ctx context.Context, userID string) (map[string]any, error) {
	var configStr string
	err := p.pool.QueryRow(ctx, queries.GetUserConfig, userID).Scan(&configStr)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(configStr), &config); err != nil {
		return nil, fmt.Errorf("failed to parse user config jsonb: %w", err)
	}
	return config, nil
}

// UpdateUserConfig inserts or updates a user's JSONB config.
func (p *Postgres) UpdateUserConfig(ctx context.Context, userID string, config map[string]any) error {
	configBytes, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, queries.UpsertUserConfig, userID, configBytes)
	return err
}
