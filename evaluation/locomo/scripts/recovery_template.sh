#!/usr/bin/env bash
# recovery_template.sh — hardened multi-phase bash template for long-running eval/recovery jobs.
#
# BACKGROUND
# ----------
# M7 Stage 3 (2026-04-25): a recovery script using plain `set -euo pipefail` died silently
# after Phase 3B because `set -e` terminates on any non-zero exit without printing context.
# Lost hours diagnosing which phase failed and why.
# See: ~/.claude/projects/-home-krolik/memory/feedback_set_e_recovery_scripts.md
#
# RULES THIS TEMPLATE ENFORCES
# -----------------------------
# 1. ERR trap fires BEFORE set -e kills the shell → logs line number + phase name.
# 2. Heartbeat goroutine writes $NAME.heartbeat every 60s with phase name + timestamp.
#    Lets operators see "we're alive, at phase N" even if the script hangs.
# 3. Per-phase checkpoint files ($NAME.phase-N.done) let you skip already-completed
#    phases on re-run.  Check: [[ -f "$CHECKPOINT" ]] && { echo "skip"; continue; }
# 4. DONE sentinel ($NAME.DONE) — the canonical "all phases succeeded" marker.
# 5. Cleanup trap always removes the heartbeat PID file on exit (normal or error).
#
# USAGE
# -----
# 1. Copy this file: cp recovery_template.sh my_recovery_$(date +%Y%m%d).sh
# 2. Set NAME at the top (used for checkpoint/heartbeat file prefixes).
# 3. Replace the phase_N functions with your actual work.
# 4. Adjust PHASES array and the main loop if you have more/fewer phases.
# 5. chmod +x + run.
#
# LOG FILE
# --------
# All output (stdout + stderr) is tee'd to $LOG.  The ERR trap appends to $LOG too.
# View live: tail -f /tmp/$NAME.log

set -uo pipefail
# NOTE: set -e is intentionally NOT placed before the ERR trap.
# The ERR trap is installed first, then we enable errexit.

# ============================================================
# CONFIG — edit these
# ============================================================
NAME="${RECOVERY_JOB_NAME:-recovery-$(date +%Y%m%d-%H%M%S)}"
LOG="/tmp/$NAME.log"
HEARTBEAT_INTERVAL=60  # seconds between heartbeat writes

# ============================================================
# TRAPS — must be installed before set -e
# ============================================================
CURRENT_PHASE="init"
HEARTBEAT_PID=""

err_handler() {
    local lineno="$1"
    local cmd="$2"
    echo "ERROR [phase=$CURRENT_PHASE] line=$lineno cmd=$cmd at $(date -u +%Y-%m-%dT%H:%M:%SZ)" | tee -a "$LOG" >&2
    echo "FAIL  [phase=$CURRENT_PHASE] line=$lineno — see $LOG for full output" >&2
}
trap 'err_handler "$LINENO" "$BASH_COMMAND"' ERR

cleanup() {
    if [[ -n "$HEARTBEAT_PID" ]]; then
        kill "$HEARTBEAT_PID" 2>/dev/null || true
        rm -f "/tmp/$NAME.heartbeat"
    fi
}
trap cleanup EXIT

# Now enable errexit (after traps are installed).
set -e

# ============================================================
# LOGGING — tee stdout+stderr to log file
# ============================================================
mkdir -p "$(dirname "$LOG")"
exec > >(tee -a "$LOG") 2>&1
echo "=== $NAME start $(date -u +%Y-%m-%dT%H:%M:%SZ) ===" | tee -a "$LOG"

# ============================================================
# HEARTBEAT — background loop
# ============================================================
heartbeat_loop() {
    while true; do
        echo "HEARTBEAT phase=$CURRENT_PHASE at $(date -u +%Y-%m-%dT%H:%M:%SZ)" > "/tmp/$NAME.heartbeat"
        sleep "$HEARTBEAT_INTERVAL"
    done
}
heartbeat_loop &
HEARTBEAT_PID=$!

# ============================================================
# HELPERS
# ============================================================

# checkpoint_done: returns 0 (true) if phase N checkpoint file exists.
checkpoint_done() {
    local phase="$1"
    [[ -f "/tmp/$NAME.phase-$phase.done" ]]
}

# mark_done: writes the checkpoint file for phase N.
mark_done() {
    local phase="$1"
    touch "/tmp/$NAME.phase-$phase.done"
    echo "[phase-$phase] checkpoint written"
}

# ============================================================
# PHASES — replace with your actual work
# ============================================================

phase_1() {
    CURRENT_PHASE="1-setup"
    if checkpoint_done 1; then
        echo "[phase-1] already done — skipping"
        return
    fi
    echo "[phase-1] start: environment setup"

    # --- YOUR WORK HERE ---
    # Example: validate environment variables
    : "${MEMDB_URL:?MEMDB_URL must be set}"
    echo "[phase-1] MEMDB_URL=$MEMDB_URL"
    # --- END YOUR WORK ---

    mark_done 1
}

phase_2() {
    CURRENT_PHASE="2-ingest"
    if checkpoint_done 2; then
        echo "[phase-2] already done — skipping"
        return
    fi
    echo "[phase-2] start: data ingest"

    # --- YOUR WORK HERE ---
    # Example: run ingest with explicit error checking
    # python3 ingest.py --full --memdb-url "$MEMDB_URL"
    echo "[phase-2] ingest placeholder (replace with real command)"
    # --- END YOUR WORK ---

    mark_done 2
}

phase_3() {
    CURRENT_PHASE="3-query"
    if checkpoint_done 3; then
        echo "[phase-3] already done — skipping"
        return
    fi
    echo "[phase-3] start: query / evaluation"

    # --- YOUR WORK HERE ---
    # Example: run query
    # python3 query.py --full --memdb-url "$MEMDB_URL" --out /tmp/preds.json
    echo "[phase-3] query placeholder (replace with real command)"
    # --- END YOUR WORK ---

    mark_done 3
}

phase_4() {
    CURRENT_PHASE="4-score"
    if checkpoint_done 4; then
        echo "[phase-4] already done — skipping"
        return
    fi
    echo "[phase-4] start: scoring"

    # --- YOUR WORK HERE ---
    # Example: score
    # python3 score.py --predictions /tmp/preds.json --out /tmp/result.json
    echo "[phase-4] score placeholder (replace with real command)"
    # --- END YOUR WORK ---

    mark_done 4
}

# ============================================================
# MAIN — phases run sequentially; each is idempotent
# ============================================================
PHASES=(1 2 3 4)

for phase in "${PHASES[@]}"; do
    "phase_$phase"
done

# All phases succeeded.
CURRENT_PHASE="done"
touch "/tmp/$NAME.DONE"
echo "=== $NAME DONE $(date -u +%Y-%m-%dT%H:%M:%SZ) ==="
echo "    log:  $LOG"
echo "    done: /tmp/$NAME.DONE"
