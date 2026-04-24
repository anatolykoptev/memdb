#!/usr/bin/env bash
# run.sh — end-to-end LoCoMo eval orchestrator for memdb-go.
#
# Requires memdb-go (+ postgres + redis + embed-server) to be RUNNING
# and reachable at MEMDB_URL. Fully ephemeral stack setup is TODO
# (see README). This script handles ingest → query → score → baseline save.
#
# Env:
#   MEMDB_URL             memdb-go base URL (default: http://localhost:8080)
#   MEMDB_API_KEY         Bearer token (plain key matching MASTER_KEY_HASH)
#   MEMDB_SERVICE_SECRET  X-Service-Secret alternative (from memdb-go env)
#                         — at least one of MEMDB_API_KEY / _SERVICE_SECRET required
#   LOCOMO_FULL=1         run against full locomo10.json (else: sample)
#   LOCOMO_SKIP_CHAT=1    skip /product/chat/complete, score retrieval only
#   LLM_URL, LLM_API_KEY  for embedding-based semsim (optional)
#   OUT_SUFFIX            override default <commit-sha> output filename

set -euo pipefail

EVAL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$EVAL_DIR/../.." && pwd)"
RESULTS_DIR="$EVAL_DIR/results"
mkdir -p "$RESULTS_DIR"

MEMDB_URL="${MEMDB_URL:-http://localhost:8080}"

if [[ "${LOCOMO_FULL:-0}" == "1" ]]; then
    MODE_FLAG="--full"
    echo "==> mode: FULL LoCoMo (10 convs, ~1990 QAs)"
else
    MODE_FLAG="--sample"
    echo "==> mode: SAMPLE (2 convs, 20 QAs)"
fi

# 0. Sanity: memdb-go reachable
echo "==> checking memdb-go at $MEMDB_URL"
if ! curl -sf --max-time 5 "$MEMDB_URL/health" >/dev/null 2>&1; then
    echo "!! memdb-go not reachable at $MEMDB_URL/health"
    echo "!! start it first: cd ~/deploy/krolik-server && docker compose up -d memdb-go"
    exit 2
fi

SHA=$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")
SUFFIX="${OUT_SUFFIX:-$SHA}"
PREDS_OUT="$RESULTS_DIR/predictions-$SUFFIX.json"
SCORE_OUT="$RESULTS_DIR/$SUFFIX.json"

# 1. Ingest
echo "==> [1/3] ingest"
python3 "$EVAL_DIR/ingest.py" $MODE_FLAG --memdb-url "$MEMDB_URL"

# Give memdb-go scheduler a moment to settle (async_mode=sync is already blocking,
# but background consolidation can still be in flight).
sleep 2

# 2. Query
echo "==> [2/3] query"
QUERY_ARGS=()
if [[ "${LOCOMO_SKIP_CHAT:-0}" == "1" ]]; then
    QUERY_ARGS+=("--skip-chat")
fi
python3 "$EVAL_DIR/query.py" $MODE_FLAG \
    --memdb-url "$MEMDB_URL" \
    --out "$PREDS_OUT" \
    "${QUERY_ARGS[@]}"

# 3. Score
echo "==> [3/3] score"
SCORE_ARGS=()
if [[ "${LOCOMO_SKIP_CHAT:-0}" == "1" ]]; then
    SCORE_ARGS+=("--retrieval-only")
fi
if [[ -z "${LLM_URL:-}" || -z "${LLM_API_KEY:-}" ]]; then
    SCORE_ARGS+=("--no-embed")
fi
python3 "$EVAL_DIR/score.py" \
    --predictions "$PREDS_OUT" \
    --out "$SCORE_OUT" \
    "${SCORE_ARGS[@]}"

echo ""
echo "==> done. result: $SCORE_OUT"
echo "==> to set this as the new baseline:"
echo "    cp $SCORE_OUT $RESULTS_DIR/baseline-v1.1.0.json"
