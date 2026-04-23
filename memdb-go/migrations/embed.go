// Package migrations embeds versioned SQL files, read at startup by
// internal/db.(*Postgres).RunMigrations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
