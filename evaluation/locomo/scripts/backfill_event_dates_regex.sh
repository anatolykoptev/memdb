#!/usr/bin/env bash
# backfill_event_dates_regex.sh — wraps backfill_event_dates_regex.sql in a
# `docker exec` invocation so the operator does not have to memorise the
# psql connection string.
#
# What it does: runs the regex-only event_dates enrichment pass against
# every Memory row in the running postgres container. NO LLM calls — stays
# inside the raw-mode "no LLM" contract.
#
# Env:
#   POSTGRES_CONTAINER   default: postgres (the docker compose service name)
#   POSTGRES_USER        default: memos
#   POSTGRES_DB          default: memos
#   DRY_RUN=1            wrap in BEGIN; ROLLBACK; — prints affected rows but
#                        leaves the data unchanged. Useful for first runs.
#
# Exit 0 on success, non-zero on any psql error. Output streams the
# RETURNING new_dates rows to stdout so the operator can spot-check the
# update.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SQL_FILE="$SCRIPT_DIR/backfill_event_dates_regex.sql"

POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-postgres}"
POSTGRES_USER="${POSTGRES_USER:-memos}"
POSTGRES_DB="${POSTGRES_DB:-memos}"

if [[ ! -f "$SQL_FILE" ]]; then
    echo "!! SQL file not found: $SQL_FILE" >&2
    exit 2
fi

if ! docker ps --format '{{.Names}}' | grep -qx "$POSTGRES_CONTAINER"; then
    echo "!! Postgres container '$POSTGRES_CONTAINER' not running" >&2
    echo "!! Override with POSTGRES_CONTAINER=<name> bash $(basename "$0")" >&2
    exit 3
fi

echo "==> regex event_dates backfill"
echo "    container: $POSTGRES_CONTAINER"
echo "    db:        $POSTGRES_USER@$POSTGRES_DB"
echo "    dry_run:   ${DRY_RUN:-0}"

if [[ "${DRY_RUN:-0}" == "1" ]]; then
    {
        echo "BEGIN;"
        cat "$SQL_FILE"
        echo "ROLLBACK;"
    } | docker exec -i "$POSTGRES_CONTAINER" psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"
else
    docker exec -i "$POSTGRES_CONTAINER" psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" < "$SQL_FILE"
fi

echo "==> done"
