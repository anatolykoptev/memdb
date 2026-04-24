#!/usr/bin/env bash
# test-migrations-fresh-db.sh — prove RunMigrations bootstraps a blank Postgres+AGE+pgvector.
#
# Usage:   bash scripts/test-migrations-fresh-db.sh
# Requires: krolik-postgres-age:17 image built locally (prod image).
#           Set MEMDB_TEST_PORT to override the mapped port (default 55432).
#           Set MEMDB_TEST_KEEP=1 to keep the container on failure for debugging.

set -euo pipefail

PORT="${MEMDB_TEST_PORT:-55432}"
CONTAINER="memdb-migration-test-pg"
IMAGE="krolik-postgres-age:17"
DSN="postgres://memos:test@localhost:${PORT}/memos?sslmode=disable"

cleanup() {
    local rc=$?
    if [[ "${MEMDB_TEST_KEEP:-0}" == "1" && "$rc" -ne 0 ]]; then
        echo "!! test failed, container kept for debugging: $CONTAINER (port $PORT)"
        return
    fi
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker rm -f "$CONTAINER" >/dev/null 2>&1 || true

echo "==> Starting fresh Postgres $IMAGE on port $PORT"
docker run -d --rm --name "$CONTAINER" \
    -e POSTGRES_USER=memos -e POSTGRES_PASSWORD=test -e POSTGRES_DB=memos \
    -p "${PORT}:5432" \
    "$IMAGE" >/dev/null

echo "==> Waiting for readiness"
for i in $(seq 1 30); do
    if docker exec "$CONTAINER" pg_isready -U memos -d memos >/dev/null 2>&1; then
        break
    fi
    sleep 1
    if [[ "$i" -eq 30 ]]; then
        echo "!! Postgres did not become ready in 30s"
        docker logs "$CONTAINER" | tail -30
        exit 1
    fi
done

echo "==> Running migration-test binary against $DSN"
cd "$(dirname "$0")/.."
MEMDB_TEST_DSN="$DSN" go run ./cmd/migration-test

echo "==> Asserting schema state"

assert_sql() {
    local label="$1"; shift
    local sql="$1"
    local expected="$2"
    local got
    got=$(docker exec "$CONTAINER" psql -U memos memos -tAc "$sql" 2>/dev/null)
    if [[ "$got" != "$expected" ]]; then
        echo "!! FAIL [$label]: expected '$expected', got '$got'"
        exit 1
    fi
    echo "   ok [$label] = $got"
}

assert_sql "schema_migrations row count" \
    "SELECT count(*) FROM memos_graph.schema_migrations" "12"

assert_sql "age extension installed" \
    "SELECT 1 FROM pg_extension WHERE extname='age'" "1"

assert_sql "vector extension installed" \
    "SELECT 1 FROM pg_extension WHERE extname='vector'" "1"

assert_sql "AGE graph exists" \
    "SELECT 1 FROM ag_catalog.ag_graph WHERE name='memos_graph'" "1"

assert_sql "tsvector column exists" \
    "SELECT 1 FROM information_schema.columns WHERE table_schema='memos_graph' AND table_name='Memory' AND column_name='properties_tsvector_zh'" "1"

assert_sql "HNSW embedding index exists" \
    "SELECT 1 FROM pg_indexes WHERE schemaname='memos_graph' AND indexname='idx_memory_embedding'" "1"

assert_sql "tsvector GIN index exists" \
    "SELECT 1 FROM pg_indexes WHERE schemaname='memos_graph' AND indexname='idx_memory_tsvector_zh'" "1"

assert_sql "tsvector trigger exists" \
    "SELECT 1 FROM pg_trigger WHERE tgname='trg_update_tsvector_zh'" "1"

echo "==> Second run (idempotency check)"
MEMDB_TEST_DSN="$DSN" go run ./cmd/migration-test

assert_sql "schema_migrations still 12 after re-run" \
    "SELECT count(*) FROM memos_graph.schema_migrations" "12"

echo ""
echo "All fresh-DB bootstrap assertions passed."
