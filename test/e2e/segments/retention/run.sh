#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Retention segment: the telemetry retention janitor runs and obeys its policy.
# (new; backfills the "nothing removes old telemetry files" gap)
#
# What this proves, and what it does not:
#   - The Mixer starts the janitor at boot and the startup sweep runs.
#   - A parquet file older than the retention window is deleted.
#   - A parquet file inside the window survives (the control: a janitor that
#     deletes everything would pass a test that only checks the old file).
#   - The negative control: with retention_days set past the planted file's
#     age, the old file survives, which proves the config is read and not a
#     hardcoded default.
#
# It does NOT prove DuckDB rows age out with the files (they do not; the
# janitor deletes parquet files only), nor a size cap (not implemented).
#
# Runs on its own ports (8112/4311 — see the port table in the internal
# e2e-consolidation design notes). Cleanup here is PID-tracked and
# port-scoped only, never `pkill -f "bin/fluxrig"`.
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../../.." && pwd)"

source "${ROOT_DIR}/test/e2e/utils/e2e_utils.sh"

setup_workspace "retention" "$BASE_DIR"

banner "Telemetry Retention Janitor"

API_PORT=8112
SNAKE_PORT=4311
API_URL="http://localhost:${API_PORT}/api/v1"

MIXER_LOG="$WORK_DIR/mixer/mixer.stdout"
MIXER_FILE_LOG="$WORK_DIR/mixer/logs/mixer.log"
TELEMETRY_DIR="$WORK_DIR/mixer/data/telemetry"
MIXER_PID=""

cleanup() {
    echo ""
    log_info "Shutting down..."
    if [ -n "$MIXER_PID" ]; then
        kill -9 $MIXER_PID 2>/dev/null || true
        wait $MIXER_PID 2>/dev/null || true
    fi
}
trap cleanup EXIT

# 0. This segment owns 8112/4311 exclusively; clearing them is scoped to
# those two ports, never a blanket process-name kill.
lsof -ti :${API_PORT} | xargs kill -9 2>/dev/null || true
lsof -ti :${SNAKE_PORT} | xargs kill -9 2>/dev/null || true

if [ -z "${FLUXRIG_E2E_SKIP_BUILD:-}" ]; then
    log_info "Building..."
    cd "${ROOT_DIR}"
    make build > "${WORK_DIR}/mixer/logs/build.log" 2>&1 || fail "Build failed. Check ${WORK_DIR}/mixer/logs/build.log"
fi

gen_keys "$WORK_DIR/mixer/data/cluster.key"

# start_mixer <config_source> : boot the Mixer with the given toml, wait for health
start_mixer() {
    local config_source="$1"
    cp "${config_source}" "${WORK_DIR}/mixer/mixer.toml"
    cd "${WORK_DIR}/mixer"
    "$(bin_dir)/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
    MIXER_PID=$!
    cd "${BASE_DIR}"
    wait_for_mixer "$API_URL"
}

stop_mixer() {
    if [ -n "$MIXER_PID" ]; then
        kill -9 $MIXER_PID 2>/dev/null || true
        wait $MIXER_PID 2>/dev/null || true
        MIXER_PID=""
    fi
}

# The store path the janitor walks is <store.dir>/telemetry. The files the
# janitor targets live under telemetry/<kind>/YYYY/MM/DD/HH/, so plant them
# there. The name carries a unix-nanos timestamp in the shape the store
# writes (type_<nanos>.parquet); the mtime is set to match, because the
# janitor reads the name and falls back to the mtime.
plant_parquet() {
    local rel="$1"
    local age_days="$2"
    local path="${TELEMETRY_DIR}/${rel}"
    mkdir -p "$(dirname "$path")"
    printf 'planted' > "$path"

    local ts
    ts=$("$DB_CLI" -noheader -csv -c "SELECT (epoch_us(now()) - ${age_days}*86400000000);" | tr -d '\r')
    # Timestamp in the file name is unix nanos.
    local nanos=$(( ts * 1000 ))
    local dir
    dir="$(dirname "$path")"
    mv "$path" "${dir}/logs_${nanos}.parquet"
    path="${dir}/logs_${nanos}.parquet"
    # Align the mtime with the intended age so both signals agree.
    local epoch_sec=$(( ts / 1000000 ))
    touch -d "@${epoch_sec}" "$path"
    echo "$path"
}

DB_CLI="duckdb"
command -v "$DB_CLI" >/dev/null 2>&1 || fail "duckdb CLI not found; it is required for this segment."

# ==============================================================================
section "Phase 1: default retention (30 days) deletes an old file, keeps a fresh one"
# ==============================================================================

OLD_FILE=$(plant_parquet "logs/2026/01/01/00/old.parquet" 400)
FRESH_FILE=$(plant_parquet "logs/2026/09/20/12/fresh.parquet" 1)
log_info "Planted old file:   $OLD_FILE"
log_info "Planted fresh file: $FRESH_FILE"

start_mixer "${BASE_DIR}/mixer/mixer.toml"
log_info "Mixer up; waiting for the startup retention sweep..."

# The janitor sweeps once at startup before the ticker. Poll for the old
# file to disappear rather than sleeping a fixed amount.
GONE=0
for ((i=1;i<=30;i++)); do
    if [ ! -f "$OLD_FILE" ]; then
        GONE=1
        break
    fi
    sleep 1
done
[ $GONE -eq 1 ] || { tail -n 40 "$MIXER_LOG"; fail "Old parquet file was not deleted by the startup sweep: $OLD_FILE"; }
log_success "Old file deleted by the retention sweep."

[ -f "$FRESH_FILE" ] || fail "Fresh parquet file was deleted; the janitor is not respecting the window: $FRESH_FILE"
log_success "Fresh file survived the sweep (control holds)."

grep -q "Telemetry retention janitor started" "$MIXER_FILE_LOG" || fail "The Mixer log does not show the janitor starting."
log_success "Mixer log confirms the janitor started."

stop_mixer

# ==============================================================================
section "Phase 2: negative control — a long retention keeps the old file"
# ==============================================================================

# A second old file, and a config that keeps everything for 10 years. If the
# file is deleted here, retention_days is not actually being read.
KEEP_FILE=$(plant_parquet "logs/2026/01/01/00/keep.parquet" 400)
log_info "Planted keep file: $KEEP_FILE"

cp "${BASE_DIR}/mixer/mixer_long_retention.toml" "${WORK_DIR}/mixer/mixer_long.toml"
start_mixer "${WORK_DIR}/mixer/mixer_long.toml"

# Give the startup sweep a bounded window to run, then assert the file is
# still there. Poll for the janitor's own log line so this is not a blind
# sleep.
SWEPT=0
for ((i=1;i<=30;i++)); do
    if grep -q "janitor starting cleanup" "$MIXER_FILE_LOG"; then
        SWEPT=1
        break
    fi
    sleep 1
done
[ $SWEPT -eq 1 ] || { tail -n 40 "$MIXER_LOG"; fail "The janitor did not log its sweep in phase 2."; }
# A short grace period after the sweep starts, so a deletion would have
# happened before the assertion.
sleep 2

[ -f "$KEEP_FILE" ] || fail "retention_days=3650 was ignored; the old file was deleted: $KEEP_FILE"
log_success "Old file kept under the long retention window (config is read)."

grep -q "retention_days=3650" "$MIXER_FILE_LOG" || log_warn "Could not confirm retention_days=3650 in the log; the survival assertion is the real check."

stop_mixer

banner "SUCCESS: All checks passed."
