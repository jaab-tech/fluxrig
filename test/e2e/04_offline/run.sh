#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Offline Mode E2E Test
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "offline" "$BASE_DIR"

banner "Offline Mode E2E Test"

# Config
MIXER_CONFIG="$BASE_DIR/mixer/mixer.toml"
RACK_CONFIG="$BASE_DIR/rack/rack.toml"
API_URL="http://localhost:8090/api/v1"

MIXER_LOG="$WORK_DIR/mixer/logs/mixer.log"
RACK_LOG="$WORK_DIR/rack/logs/rack.log"

# Deadlines for the polling waits. The offline one is the contract under test: a
# Rack holding a passport must be running without the Mixer well inside it, and it
# stays clear of snake.offline_start_timeout (3s by default) plus process start.
ONLINE_TIMEOUT=30
OFFLINE_TIMEOUT=15
# A Rack that started offline probes the bus every snake.offline_retry_interval (5s
# by default), restarts its session and sends its first heartbeat.
RECONNECT_TIMEOUT=30

MIXER_PID=""
RACK_PID=""

cleanup() {
    log_info "Shutting down..."
    if [ -n "$MIXER_PID" ]; then kill -9 $MIXER_PID 2>/dev/null || true; wait $MIXER_PID 2>/dev/null || true; fi
    if [ -n "$RACK_PID" ]; then kill -9 $RACK_PID 2>/dev/null || true; wait $RACK_PID 2>/dev/null || true; fi
    pkill -f "bin/fluxrig" || true
}
trap cleanup EXIT

# 0. Prep
pkill -f "bin/fluxrig" || true
# ensure ports are free
lsof -ti :8090 | xargs kill -9 2>/dev/null || true

# 1. Start Mixer
log_info "--- Phase 1: Online Enrollment ---"
log_info "Generating Keys..."
"${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "$WORK_DIR/mixer/data/cluster.key" > /dev/null

log_info "Starting Mixer..."
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"
wait_for_port 8090 30 || fail "Mixer API did not come up."

log_info "Starting Rack (Online)..."
cp "${BASE_DIR}/rack/rack.toml" "${WORK_DIR}/rack/rack.toml"
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PID=$!
cd "${BASE_DIR}"

# Wait for the Passport and for a heartbeat
if wait_for_log "$RACK_LOG" "Passport Saved" "$ONLINE_TIMEOUT"; then
    log_success "Passport Acquired."
else
    cat "$RACK_LOG"
    fail "Failed to acquire passport."
fi

if wait_for_log "$RACK_LOG" "Sent Heartbeat" "$ONLINE_TIMEOUT"; then
    log_success "Online Activity Verified (Sent Heartbeats)."
else
    cat "$RACK_LOG"
    fail "Failed to send heartbeats (No online activity detected)."
fi

# 3. Stop Everything
log_info "Stopping World..."
kill -9 $MIXER_PID 2>/dev/null || true
wait $MIXER_PID 2>/dev/null || true
MIXER_PID=""

kill -9 $RACK_PID 2>/dev/null || true
wait $RACK_PID 2>/dev/null || true
RACK_PID=""

log_success "Environment Stopped. Mixer is DEAD."

# 4. Phase 2: Offline Startup
log_info "--- Phase 2: Offline Startup ---"
log_info "Starting Rack (Offline)..."
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" >> "rack.stdout" 2>&1 &
RACK_PID=$!
cd "${BASE_DIR}"

# 5. Verification
LOG="$WORK_DIR/rack/rack.stdout"

# The Rack must reach offline mode within the deadline, not after the bus retry
# budget: wait for that line first, then check what else it logged.
wait_for_log "$LOG" "Starting in OFFLINE Mode" "$OFFLINE_TIMEOUT" || true

# Check 1: Loaded Cached Passport
if grep -q "Loaded Cached Passport" "$LOG"; then
    log_success "(1/3) Rack loaded cached passport."
else
    cat "$LOG"
    fail "(1/3) Rack failed to load passport."
fi

# Check 2: Offline Mode
if grep -q "Starting in OFFLINE Mode" "$LOG"; then
    log_success "(2/3) Rack detected Offline Mode."
else
    cat "$LOG"
    fail "(2/3) Rack did not report Offline Mode."
fi

# Check 3: Process is still running
if ps -p $RACK_PID > /dev/null; then
    log_success "(3/3) Rack process is still ALIVE."
else
    cat "$LOG"
    fail "(3/3) Rack process DIED."
fi

# 6. Phase 3: The Mixer returns. The Rack that started offline must find it on its
# own, without being restarted.
log_info "--- Phase 3: Mixer Returns ---"
RACK_STDOUT_SEEN=$(wc -l < "$LOG")
RACK_LOG_SEEN=$(wc -l < "$RACK_LOG")

log_info "Restarting Mixer..."
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig-mixer" -c "mixer.toml" >> "mixer.stdout" 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"

if wait_for_log "$LOG" "Bus reachable again" "$RECONNECT_TIMEOUT" "$RACK_STDOUT_SEEN"; then
    log_success "(1/2) Rack noticed the Mixer is back."
else
    cat "$LOG"
    fail "(1/2) Rack never noticed the Mixer is back."
fi

if wait_for_log "$RACK_LOG" "Sent Heartbeat" "$RECONNECT_TIMEOUT" "$RACK_LOG_SEEN"; then
    log_success "(2/2) Rack is online again (Sent Heartbeats)."
else
    cat "$RACK_LOG"
    fail "(2/2) Rack did not go back online."
fi

banner "Offline Mode Verified"
