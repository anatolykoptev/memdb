-- MemDB Postgres init: install required extensions.
-- This runs once on first container startup (docker-entrypoint-initdb.d/).
-- memdb-go runs its own schema migrations on boot; this file only provisions extensions.

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS age;
LOAD 'age';
SET search_path = ag_catalog, "$user", public;
