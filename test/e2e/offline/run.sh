#!/bin/bash

# Copyright 2025 JAAB Tech SAS, Uruguay
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

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

MIXER_PID=""
RACK_PID=""

cleanup() {
    log_info "Shutting down..."
    if [ -n "$MIXER_PID" ]; then kill $MIXER_PID 2>/dev/null || true; wait $MIXER_PID 2>/dev/null || true; fi
    if [ -n "$RACK_PID" ]; then kill $RACK_PID 2>/dev/null || true; wait $RACK_PID 2>/dev/null || true; fi
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
sleep 2

log_info "Starting Rack (Online)..."
cp "${BASE_DIR}/rack/rack.toml" "${WORK_DIR}/rack/rack.toml"
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PID=$!
cd "${BASE_DIR}"

# Wait for Passport and Heartbeats (Interval is 2s, wait 6s for at least 2 heartbeats)
sleep 6

if grep -q "Passport Saved" "$RACK_LOG"; then
    log_success "Passport Acquired."
else
    cat "$RACK_LOG"
    fail "Failed to acquire passport."
fi

if grep -q "Sent Heartbeat" "$RACK_LOG"; then
    log_success "Online Activity Verified (Sent Heartbeats)."
else
    cat "$RACK_LOG"
    fail "Failed to send heartbeats (No online activity detected)."
fi

# 3. Stop Everything
log_info "Stopping World..."
kill $MIXER_PID
wait $MIXER_PID 2>/dev/null || true
MIXER_PID=""

kill $RACK_PID
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

sleep 2

# 5. Verification
LOG="$WORK_DIR/rack/rack.stdout"

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

banner "Offline Mode Verified"
