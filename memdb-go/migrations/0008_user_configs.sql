-- MemDB migration 0008: user_configs table
-- Date: 2026-04-23
-- Ports EnsureUserConfigsTable from internal/db/postgres_config.go into versioned runner.
-- Stores per-user JSONB configuration blobs (GetUserConfig / UpdateUserConfig).
-- Idempotent: IF NOT EXISTS on CREATE TABLE.

CREATE TABLE IF NOT EXISTS user_configs (
    user_id TEXT PRIMARY KEY,
    config  JSONB NOT NULL DEFAULT '{}'::jsonb
);
