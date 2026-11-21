#!/usr/bin/env bash
# eval_guard.sh - target guard for shell harnesses. Source it, do not execute it.
# Python harnesses use eval_guard.py; the rules are the same (see its docstring):
# no default target, /health must report env=eval, requests carry the marker.
#
#   source "$EVAL_DIR/../eval_guard.sh"
#   eval_guard_target "${MEMDB_URL:-}"      # exits 2 / 3 instead of returning on failure
#
# Marker header sent on every request a harness makes; a non-eval server answers 403.
EVAL_HARNESS_HEADER="X-MemDB-Eval-Harness: 1"

eval_guard_target() {
    local url="${1:-}"
    if [[ -z "$url" ]]; then
        echo "!! no target: set MEMDB_URL. There is no default; start an isolated instance" >&2
        echo "!! with: docker compose -f docker/docker-compose.eval.yml up -d --build" >&2
        exit 2
    fi
    url="${url%/}"
    local body
    if ! body="$(curl -sf --max-time 5 "$url/health" 2>/dev/null)"; then
        echo "!! cannot read $url/health - refusing to run" >&2
        exit 3
    fi
    if ! printf '%s' "$body" | grep -Eq '"env"[[:space:]]*:[[:space:]]*"eval"'; then
        echo "!! $url does not report env=eval - refusing to write benchmark data to it" >&2
        echo "!! use an instance started with MEMDB_ENV=eval (docker/docker-compose.eval.yml)" >&2
        exit 3
    fi
}

# eval_guard_container NAME - for scripts that talk to Postgres through `docker exec`:
# the container must belong to the memdb-eval compose project, never to a real stack.
eval_guard_container() {
    local name="${1:-}" project
    if [[ -z "$name" ]]; then
        echo "!! no Postgres container given (no default). Use the eval stack's container:" >&2
        echo "!!   POSTGRES_CONTAINER=\$(docker compose -p memdb-eval -f docker/docker-compose.eval.yml ps -q postgres)" >&2
        exit 2
    fi
    project="$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project" }}' "$name" 2>/dev/null || true)"
    if [[ "$project" != "memdb-eval" ]]; then
        echo "!! container $name is not part of the memdb-eval compose project (project=${project:-<none>})" >&2
        exit 3
    fi
}
