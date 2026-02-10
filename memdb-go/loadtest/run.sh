#!/usr/bin/env bash
# memdb-go load testing script using vegeta
# Usage: ./run.sh [scenario] [options]
#
# Scenarios:
#   health     - GET /health (baseline latency)
#   search     - POST /product/search (main workload)
#   add        - POST /product/add in fast mode
#   stream     - POST /product/chat/stream (SSE)
#   ratelimit  - Burst test to verify 429 responses
#   all        - Run health + search sequentially
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
    local tmp_target
    tmp_target=$(mktemp)
    envsubst < "${target_file}" > "$tmp_target"

    # Replace port in target file
    sed -i "s|localhost:8080|${HOST}:${PORT}|g" "$tmp_target"

    # Run vegeta attack
    vegeta attack \
        -targets="$tmp_target" \
        -rate="${rate}" \
        -duration="${duration}" \
        -timeout="30s" \
        -workers=10 \
    | tee "${result_prefix}.bin" \
    | vegeta report \
    | tee "${result_prefix}.txt"

    # Generate plot
    vegeta plot < "${result_prefix}.bin" > "${result_prefix}.html" 2>/dev/null || true

    # JSON report for programmatic comparison
    vegeta report -type=json < "${result_prefix}.bin" > "${result_prefix}.json" 2>/dev/null || true

    rm -f "$tmp_target"
    echo ""
}

run_ratelimit_test() {
    echo "=== Rate Limit Burst Test ==="
    echo "  Sending 200 requests at 200 req/s to trigger rate limiting"

    local tmp_target
    tmp_target=$(mktemp)
    envsubst < "${TARGETS_DIR}/health.txt" > "$tmp_target"
    sed -i "s|localhost:8080|${HOST}:${PORT}|g" "$tmp_target"

    local result_prefix="${RESULTS_DIR}/ratelimit_${TIMESTAMP}"

    vegeta attack \
        -targets="$tmp_target" \
        -rate=200 \
        -duration="1s" \
        -timeout="5s" \
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

    rm -f "$tmp_target"
    echo ""
}

case "$SCENARIO" in
    health)
        run_scenario "health" "${TARGETS_DIR}/health.txt"
        ;;
    search)
        run_scenario "search" "${TARGETS_DIR}/search.txt"
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
        ;;
    compare)
        echo "=== Comparing Go gateway vs Python direct ==="
        echo ""
        PORT=8080
        run_scenario "go_health" "${TARGETS_DIR}/health.txt"
        run_scenario "go_search" "${TARGETS_DIR}/search.txt"
        PORT=8000
        run_scenario "py_health" "${TARGETS_DIR}/health.txt"
        run_scenario "py_search" "${TARGETS_DIR}/search.txt"
        echo "=== Comparison complete ==="
        echo "Results in: ${RESULTS_DIR}/"
        echo "View HTML plots for latency distribution"
        ;;
    *)
        echo "Unknown scenario: $SCENARIO"
        echo "Available: health, search, add, stream, ratelimit, all, compare"
        exit 1
        ;;
esac

echo "Done. Results in: ${RESULTS_DIR}/"
