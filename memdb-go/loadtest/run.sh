#!/usr/bin/env bash
# memdb-go load testing script using vegeta
# Usage: ./run.sh [scenario] [options]
#
# Scenarios:
#   health     - GET /health (baseline latency)
#   search     - POST /product/search (main workload)
#   get_all    - POST /product/get_all (native DB read)
#   users      - GET /product/users (native DB read)
#   add        - POST /product/add in fast mode
#   stream     - POST /product/chat/stream (SSE)
#   ratelimit  - Burst test to verify 429 responses
#   all        - Run health + search + get_all sequentially
#
# Options:
#   -r RATE      Requests per second (default: 10)
#   -d DURATION  Test duration (default: 30s)
#   -g           Target Go gateway at :8080 (default)
#   -p           Target Python backend at :8000
#   -o DIR       Output directory (default: ./results)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGETS_DIR="${SCRIPT_DIR}/targets"
RESULTS_DIR="${SCRIPT_DIR}/results"

# Defaults
RATE=10
DURATION="30s"
PORT=8080
HOST="localhost"
SCENARIO="${1:-all}"
shift 2>/dev/null || true

# Parse options
while getopts "r:d:gpo:" opt 2>/dev/null; do
    case $opt in
        r) RATE="$OPTARG" ;;
        d) DURATION="$OPTARG" ;;
        g) PORT=8080 ;;
        p) PORT=8000 ;;
        o) RESULTS_DIR="$OPTARG" ;;
        *) ;;
    esac
done

# Check vegeta is installed
if ! command -v vegeta &>/dev/null; then
    echo "Error: vegeta not found. Install with: go install github.com/tsenart/vegeta/v12@latest"
    exit 1
fi

# Check SERVICE_SECRET is set
if [ -z "${SERVICE_SECRET:-}" ]; then
    echo "Error: SERVICE_SECRET env var not set"
    echo "  export SERVICE_SECRET=your-secret-here"
    exit 1
fi

mkdir -p "$RESULTS_DIR"

TIMESTAMP=$(date +%Y%m%d_%H%M%S)

run_scenario() {
    local name="$1"
    local target_file="$2"
    local rate="${3:-$RATE}"
    local duration="${4:-$DURATION}"
    local result_prefix="${RESULTS_DIR}/${name}_${TIMESTAMP}"

    echo "=== ${name} ==="
    echo "  Target: :${PORT}, Rate: ${rate} req/s, Duration: ${duration}"

    # Substitute variables in target file
    local tmp_dir
    tmp_dir=$(mktemp -d)
    envsubst < "${target_file}" > "$tmp_dir/target.txt"

    # Replace port in target file
    sed -i "s|localhost:8080|${HOST}:${PORT}|g" "$tmp_dir/target.txt"

    # Copy body files to temp dir so vegeta can find them
    cp "${TARGETS_DIR}"/*-body.json "$tmp_dir/" 2>/dev/null || true

    # Run vegeta attack (from tmp_dir so @body.json refs resolve)
    (cd "$tmp_dir" && vegeta attack \
        -targets="target.txt" \
        -rate="${rate}" \
        -duration="${duration}" \
        -timeout="30s" \
        -workers=10) \
    | tee "${result_prefix}.bin" \
    | vegeta report \
    | tee "${result_prefix}.txt"

    # Generate plot
    vegeta plot < "${result_prefix}.bin" > "${result_prefix}.html" 2>/dev/null || true

    # JSON report for programmatic comparison
    vegeta report -type=json < "${result_prefix}.bin" > "${result_prefix}.json" 2>/dev/null || true

    rm -rf "$tmp_dir"
    echo ""
}

run_ratelimit_test() {
    echo "=== Rate Limit Burst Test ==="
    echo "  Sending 200 requests at 200 req/s to trigger rate limiting"

    local tmp_dir
    tmp_dir=$(mktemp -d)
    envsubst < "${TARGETS_DIR}/health.txt" > "$tmp_dir/target.txt"
    sed -i "s|localhost:8080|${HOST}:${PORT}|g" "$tmp_dir/target.txt"

    local result_prefix="${RESULTS_DIR}/ratelimit_${TIMESTAMP}"

    (cd "$tmp_dir" && vegeta attack \
        -targets="target.txt" \
        -rate=200 \
        -duration="1s" \
        -timeout="5s") \
    | tee "${result_prefix}.bin" \
    | vegeta report \
    | tee "${result_prefix}.txt"

    # Count 429 responses
    local total_429
    total_429=$(vegeta report -type=json < "${result_prefix}.bin" | python3 -c "
import json, sys
data = json.load(sys.stdin)
codes = data.get('status_codes', {})
print(codes.get('429', 0))
" 2>/dev/null || echo "N/A")
    echo "  429 responses: ${total_429}"

    rm -rf "$tmp_dir"
    echo ""
}

case "$SCENARIO" in
    health)
        run_scenario "health" "${TARGETS_DIR}/health.txt"
        ;;
    search)
        run_scenario "search" "${TARGETS_DIR}/search.txt"
        ;;
    get_all)
        run_scenario "get_all" "${TARGETS_DIR}/get-all.txt"
        ;;
    users)
        run_scenario "users" "${TARGETS_DIR}/users.txt"
        ;;
    add)
        run_scenario "add" "${TARGETS_DIR}/add.txt" 5
        ;;
    stream)
        run_scenario "stream" "${TARGETS_DIR}/chat-stream.txt" 2 "10s"
        ;;
    ratelimit)
        run_ratelimit_test
        ;;
    all)
        run_scenario "health" "${TARGETS_DIR}/health.txt"
        run_scenario "search" "${TARGETS_DIR}/search.txt"
        run_scenario "get_all" "${TARGETS_DIR}/get-all.txt"
        run_scenario "users" "${TARGETS_DIR}/users.txt"
        ;;
    compare)
        echo "=== Comparing Go gateway vs Python direct ==="
        echo ""
        PORT=8080
        run_scenario "go_health" "${TARGETS_DIR}/health.txt"
        run_scenario "go_get_all" "${TARGETS_DIR}/get-all.txt"
        run_scenario "go_users" "${TARGETS_DIR}/users.txt"
        run_scenario "go_search" "${TARGETS_DIR}/search.txt"
        PORT=8000
        run_scenario "py_health" "${TARGETS_DIR}/health.txt"
        run_scenario "py_get_all" "${TARGETS_DIR}/get-all.txt"
        run_scenario "py_users" "${TARGETS_DIR}/users.txt"
        run_scenario "py_search" "${TARGETS_DIR}/search.txt"
        echo "=== Comparison complete ==="
        echo "Results in: ${RESULTS_DIR}/"
        echo "View HTML plots for latency distribution"
        ;;
    *)
        echo "Unknown scenario: $SCENARIO"
        echo "Available: health, search, get_all, users, add, stream, ratelimit, all, compare"
        exit 1
        ;;
esac

echo "Done. Results in: ${RESULTS_DIR}/"
