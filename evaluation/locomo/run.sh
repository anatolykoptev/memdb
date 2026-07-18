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
#   LOCOMO_CATEGORIES     comma-separated categories (default: "1" = backward compat).
#                         Use "1,2,3,4,5" for 5-category 50-QA sample mode.
#   EMBED_URL             embed-server base URL for semsim (default: http://127.0.0.1:8082/v1)
#                         Set to "" to skip embedding and use BoW cosine instead.
#   EMBED_API_KEY         Bearer token for embed endpoint (default: "" — embed-server needs none)
#   LLM_URL, LLM_API_KEY  LLM endpoint (judge, chat). Not used for embeddings.
#   LOCOMO_LLM_JUDGE=1    run LLM judge scoring (accepts "1" or "true")
#   LOCOMO_CLEAN_BEFORE=1 hard-delete LoCoMo cubes before ingest (required for
#                         ingest-mode A/B — content_hash dedup otherwise skips
#                         every chunk that already lives in the cube)
#   OUT_SUFFIX            override default <commit-sha> output filename

set -euo pipefail

EVAL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$EVAL_DIR/../.." && pwd)"
RESULTS_DIR="$EVAL_DIR/results"
mkdir -p "$RESULTS_DIR"

MEMDB_URL="${MEMDB_URL:-http://localhost:8080}"
LOCOMO_CATEGORIES="${LOCOMO_CATEGORIES:-1}"
# Embed-server for semsim. Default: local embed-server on :8082 (no auth needed).
# Set EMBED_URL="" to disable embedding and fall back to BoW cosine.
# Must include /v1 path prefix (embed-server exposes /v1/embeddings).
EMBED_URL="${EMBED_URL:-http://127.0.0.1:8082/v1}"
EMBED_API_KEY="${EMBED_API_KEY:-}"

if [[ "${LOCOMO_FULL:-0}" == "1" ]]; then
    MODE_FLAG="--full"
    echo "==> mode: FULL LoCoMo (10 convs, ~1990 QAs, categories=${LOCOMO_CATEGORIES})"
else
    MODE_FLAG="--sample"
    if [[ "${LOCOMO_CATEGORIES}" == "1,2,3,4,5" || "${LOCOMO_CATEGORIES}" == "1,2,3,4,5," ]]; then
        echo "==> mode: SAMPLE 5-category (conv-26, 50 QAs)"
    else
        echo "==> mode: SAMPLE (conv-26, 10 category-1 QAs)"
    fi
fi

CATEGORIES_FLAG="--categories=${LOCOMO_CATEGORIES}"

# 0. Sanity: memdb-go reachable
echo "==> checking memdb-go at $MEMDB_URL"
if ! curl -sf --max-time 5 "$MEMDB_URL/health" >/dev/null 2>&1; then
    echo "!! memdb-go not reachable at $MEMDB_URL/health"
    echo "!! start it first: cd ~/deploy/server-config && docker compose up -d memdb-go"
    exit 2
fi

SHA=$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")
SUFFIX="${OUT_SUFFIX:-$SHA}"
PREDS_OUT="$RESULTS_DIR/predictions-$SUFFIX.json"
SCORE_OUT="$RESULTS_DIR/$SUFFIX.json"

# 0. Optional cube cleanup. Without this, content_hash dedup on re-ingest
# silently skips every chunk that already lives in the cube — making
# ingest-mode A/B comparisons impossible (mode=fine vs mode=raw on the
# same cube produces near-identical Memory rows after the first run).
# Default off — opt in via LOCOMO_CLEAN_BEFORE=1.
if [[ "${LOCOMO_CLEAN_BEFORE:-0}" == "1" ]]; then
    echo "==> [0/3] cleanup cubes (LOCOMO_CLEAN_BEFORE=1)"
    if [[ "${LOCOMO_FULL:-0}" == "1" ]]; then
        python3 "$EVAL_DIR/scripts/cleanup_locomo_cubes.py" --full --memdb-url "$MEMDB_URL"
    else
        python3 "$EVAL_DIR/scripts/cleanup_locomo_cubes.py" --sample --memdb-url "$MEMDB_URL"
    fi
fi

# 1. Ingest
echo "==> [1/3] ingest"
python3 "$EVAL_DIR/ingest.py" $MODE_FLAG --memdb-url "$MEMDB_URL" $CATEGORIES_FLAG

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
    $CATEGORIES_FLAG \
    "${QUERY_ARGS[@]}"

# 3. Score
echo "==> [3/3] score"
SCORE_ARGS=()
if [[ "${LOCOMO_SKIP_CHAT:-0}" == "1" ]]; then
    SCORE_ARGS+=("--retrieval-only")
fi
if [[ -z "${EMBED_URL:-}" ]]; then
    SCORE_ARGS+=("--no-embed")
else
    SCORE_ARGS+=("--embed-url" "$EMBED_URL")
    SCORE_ARGS+=("--embed-api-key" "${EMBED_API_KEY:-}")
fi
# LLM Judge headline (Memobase / mem0-comparable) is opt-in via LOCOMO_LLM_JUDGE=1|true.
# Requires LLM_URL + LLM_API_KEY to be set. Defaults to off because the judge
# itself costs N extra LLM calls (~$0.05–0.20 on full corpus).
_judge_flag="${LOCOMO_LLM_JUDGE:-0}"
if [[ ( "$_judge_flag" == "1" || "$_judge_flag" == "true" ) && -n "${LLM_URL:-}" && -n "${LLM_API_KEY:-}" ]]; then
    SCORE_ARGS+=("--llm-judge")
fi
python3 "$EVAL_DIR/score.py" \
    --predictions "$PREDS_OUT" \
    --out "$SCORE_OUT" \
    "${SCORE_ARGS[@]}"

echo ""
echo "==> done. result: $SCORE_OUT"
echo "==> to set this as the new baseline:"
echo "    cp $SCORE_OUT $RESULTS_DIR/baseline-v1.1.0.json"
