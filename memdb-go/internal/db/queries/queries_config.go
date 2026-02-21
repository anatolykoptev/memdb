package queries

// queries_config.go — SQL queries for user_configs table.
// Covers: CreateUserConfigsTable, GetUserConfig, UpsertUserConfig.

// --- User Config (user_configs table) ---

// CreateUserConfigsTable creates the user_configs table.
const CreateUserConfigsTable = `
CREATE TABLE IF NOT EXISTS user_configs (
    user_id TEXT PRIMARY KEY,
    config  JSONB NOT NULL DEFAULT '{}'::jsonb
)`

// GetUserConfig retrieves the JSONB config for a user.
// Args: $1 = user_id (text)
const GetUserConfig = `
SELECT config::text
FROM user_configs
WHERE user_id = $1`

// UpsertUserConfig inserts or updates a user's JSONB config.
// Args: $1 = user_id (text), $2 = config (jsonb)
const UpsertUserConfig = `
INSERT INTO user_configs (user_id, config)
VALUES ($1, $2::jsonb)
ON CONFLICT (user_id) DO UPDATE
SET config = EXCLUDED.config`
