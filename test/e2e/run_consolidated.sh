#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u
# Support common binary paths (Mac/Homebrew, Linux/usr/local)
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../.." && pwd)"

# ==============================================================================
# fluxrig Consolidated Regression Suite
#
# Replaces run_all.sh's one-at-a-time discovery of test/e2e/*/run.sh with an
# explicit list of segments, each on its own ports (see
# the internal e2e-consolidation design notes for the full
# design and port table), launched in parallel and aggregated here. Full
# background: same doc.
# ==============================================================================

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info() { echo -e "${CYAN}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[PASS] ✅${NC} $1"; }
log_error() { echo -e "${RED}[FAIL] ❌${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
banner() {
    echo -e "${CYAN}==============================================================================${NC}"
    echo -e "${CYAN}$1${NC}"
    echo -e "${CYAN}==============================================================================${NC}"
}

banner "fluxrig Consolidated Regression Suite"

# One build for everything: every segment below skips its own `make build`
# (FLUXRIG_E2E_SKIP_BUILD) once this has run, because concurrent `make
# build` calls from multiple segments launched in parallel would race on
# writing the same bin/fluxrig* output files.
log_info "Building..."
if ! (cd "$ROOT_DIR" && make build > /tmp/fluxrig_consolidated_build.log 2>&1); then
    log_error "Build failed. See /tmp/fluxrig_consolidated_build.log"
    exit 1
fi
export FLUXRIG_E2E_SKIP_BUILD=1
log_success "Build complete."

LOG_DIR="/tmp/fluxrig/consolidated_logs_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$LOG_DIR"
log_info "Segment logs: $LOG_DIR"

# Segments: bash scripts, each on its own ports, safe to launch together —
# but not ALL together. A full 12-way launch was tried and measured on this
# machine: multiple unrelated segments' Rack processes were SIGKILLed
# mid-run (visible as "Killed" in their logs) under the combined memory
# pressure of every segment's Mixer(s) and Rack(s) at once, exactly the
# risk flagged when this design was proposed. Real regressions, not
# flakes — group_a and group_b both failed on assertions that only fail
# under resource starvation (a CLI query racing a not-yet-flushed
# telemetry batch, a rack that hadn't reached "pending" yet). Capping
# concurrency trades some of the parallel win for actually being correct
# every time, which is the point of a regression suite.
#
# FLUXRIG_E2E_MAX_PARALLEL controls the cap; segments are ordered so each
# batch mixes a couple of heavy, long-running ones (group_a, group_b,
# hot_lane, coatcheck) with lighter ones, rather than accidentally
# clustering the heaviest segments into the same batch.
MAX_PARALLEL="${FLUXRIG_E2E_MAX_PARALLEL:-4}"

# A batch's wall-clock cost is the SLOWEST member's own time, not the sum —
# they run in parallel. That means spreading the slow, long-running
# segments across separate batches (the first instinct, and what this list
# did before being measured) is the wrong way to minimize total time: it
# turns each of several batches into a slow one. Clustering the slow
# segments into as FEW batches as the concurrency cap allows, and filling
# the remaining batches with the fast ones, minimizes the sum of batch
# maxes instead. Measured on this machine: group_a (~2m43s alone),
# hot_lane (~3min, dominated by its own Mixer-reconnect wait), coatcheck
# and group_b (~1-2min each) are the slow ones; tls_simple,
# 20_security_regression, 19_card_data_iso, offline, scenario_resume,
# io_tcp, start_without_mixer are fast by comparison. Re-measure with
# `time` after changing this list — a plausible-looking order is not the
# same as a fast one, which is exactly why the original order here was
# wrong.
SEGMENTS=(
    "group_a:$BASE_DIR/segments/group_a/run.sh"
    "hot_lane:$BASE_DIR/segments/hot_lane/run.sh"
    "coatcheck:$BASE_DIR/segments/coatcheck/run.sh"
    "group_b:$BASE_DIR/segments/group_b/run.sh"
    "start_without_mixer:$BASE_DIR/segments/start_without_mixer/run.sh"
    "tls_simple:$BASE_DIR/segments/tls_simple/run.sh"
    "offline:$BASE_DIR/segments/offline/run.sh"
    "scenario_resume:$BASE_DIR/segments/scenario_resume/run.sh"
    "20_security_regression:$BASE_DIR/20_security_regression/run.sh"
    "19_card_data_iso:$BASE_DIR/19_card_data_iso/run.sh"
    "io_tcp:$BASE_DIR/segments/io_tcp/run.sh"
)

# 12_wasm_polyglot is NOT in SEGMENTS above and never shares a batch with
# anything: its Robot suite's own setup calls force_cleanup_environment()
# (test/robot/lib/keywords/process.py), which kills every fluxrig /
# fluxrig-mixer / iso8583-tool process on the machine by exact name, plus a
# hardcoded-port sweep (8090, 8583, among others) — the same class of
# danger as `pkill -f "bin/fluxrig"`, just in the Robot/Python
# infrastructure rather than bash. Confirmed by running it: two unrelated
# segments' Mixers in the same batch shut down cleanly (a graceful
# SIGTERM handled by their own signal handling, not a crash) at the exact
# moment the wasm suite's setup ran. It needs its own dedicated batch, run
# after every other segment has finished, not folded into the round-robin
# above. Fixing force_cleanup_environment() itself (PID/port-scoped
# instead of name-based) is out of scope here: it's shared by every Robot
# suite, not owned by this consolidation.
WASM_ENTRY=""
if command -v zig >/dev/null 2>&1; then
    WASM_ENTRY="12_wasm_polyglot:$BASE_DIR/12_wasm_polyglot/run.sh"
else
    log_warn "zig not found; skipping 12_wasm_polyglot (see 'make regression' prerequisite check)."
fi

declare -A LOGFILES
FAILED=()
RUNNING_PIDS=()

cleanup() {
    # Only matters on an abnormal exit (Ctrl+C, this script killed): each
    # segment already tears down its own Mixer/Rack processes on its own
    # exit, so this just makes sure no segment script itself is left
    # running if the orchestrator dies first. Scoped to the current
    # batch's PIDs, which is all that can be running at any moment.
    for pid in "${RUNNING_PIDS[@]}"; do
        kill -9 "$pid" 2>/dev/null || true
    done
}
trap cleanup EXIT INT TERM

# run_batch NAME:SCRIPT [NAME:SCRIPT ...]: launches every entry together,
# waits for all of them, and records pass/fail for each — a fixed-size
# batch rather than a fully dynamic semaphore, so each PID's exit status is
# read exactly once by the same loop that launched it (no ambiguity from a
# `wait -n` elsewhere reaping a status this loop still needs).
run_batch() {
    declare -A batch_pids
    RUNNING_PIDS=()
    for entry in "$@"; do
        local name="${entry%%:*}" script="${entry#*:}"
        if [ ! -f "$script" ]; then
            log_warn "Segment '$name' not found at $script, skipping."
            continue
        fi
        local logfile="$LOG_DIR/${name}.log"
        LOGFILES[$name]="$logfile"
        log_info "Launching $name..."
        bash "$script" > "$logfile" 2>&1 &
        batch_pids[$name]=$!
        RUNNING_PIDS+=("$!")
    done
    for name in "${!batch_pids[@]}"; do
        if wait "${batch_pids[$name]}"; then
            log_success "Segment '$name' PASSED"
        else
            log_error "Segment '$name' FAILED (log: ${LOGFILES[$name]})"
            FAILED+=("$name")
        fi
    done
    RUNNING_PIDS=()
}

batch=()
for entry in "${SEGMENTS[@]}"; do
    batch+=("$entry")
    if [ "${#batch[@]}" -ge "$MAX_PARALLEL" ]; then
        log_info "Running a batch of ${#batch[@]} segments..."
        run_batch "${batch[@]}"
        batch=()
    fi
done
if [ "${#batch[@]}" -gt 0 ]; then
    log_info "Running a batch of ${#batch[@]} segments..."
    run_batch "${batch[@]}"
fi

if [ -n "$WASM_ENTRY" ]; then
    log_info "Running 12_wasm_polyglot alone (see the comment above WASM_ENTRY)..."
    run_batch "$WASM_ENTRY"
fi

echo ""
banner "Summary"
if [ ${#FAILED[@]} -eq 0 ]; then
    log_success "ALL SEGMENTS PASSED (${#SEGMENTS[@]} segments)"
    exit 0
else
    log_error "REGRESSION FAILED. The following segments failed:"
    for f in "${FAILED[@]}"; do
        echo -e "  - ${f} (log: ${LOGFILES[$f]})"
    done
    exit 1
fi
