#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Conflict & Identity E2E Test
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

# Helper to wait for a log pattern
wait_for_log() {
    local file=$1
    local pattern=$2
    local timeout=${3:-10}
    local name=${4:-"Pattern"}
    
    log_info "Waiting for '$name' ($pattern) in $(basename $file)..."
    local start_time=$(date +%s)
    while ! grep -q "$pattern" "$file" 2>/dev/null; do
        if [ $(($(date +%s) - start_time)) -gt $timeout ]; then
            echo "--- $file contents ---"
            cat "$file"
            fail "Timeout waiting for $name after ${timeout}s"
        fi
        sleep 0.2
    done
}

setup_workspace "conflict" "$BASE_DIR"

banner "Conflict & Identity E2E Test"

# Config
MIXER_DIR="$WORK_DIR/mixer"
RACK_A_DIR="$WORK_DIR/rack_a"
RACK_B_DIR="$WORK_DIR/rack_b"
RACK_C_DIR="$WORK_DIR/rack_c"
RACK_D_DIR="$WORK_DIR/rack_d"
RACK_E_DIR="$WORK_DIR/rack_e"

MIXER_LOG="$MIXER_DIR/mixer.stdout"
MIXER_CONFIG="$BASE_DIR/mixer/mixer.toml"

API_URL="http://localhost:8090/api/v1"

MIXER_PID=""
RACK_A_PID=""
RACK_B_PID=""
RACK_C_PID=""
RACK_D_PID=""
RACK_E_PID=""

cleanup() {
    log_info "Shutting down..."
    kill -9 $MIXER_PID 2>/dev/null || true
    kill -9 $RACK_A_PID 2>/dev/null || true
    kill -9 $RACK_B_PID 2>/dev/null || true
    kill -9 $RACK_C_PID 2>/dev/null || true
    kill -9 $RACK_D_PID 2>/dev/null || true
    kill -9 $RACK_E_PID 2>/dev/null || true
    pkill -f "bin/fluxrig" || true
}
trap cleanup EXIT

# 0. Prep
pkill -f "bin/fluxrig" || true
# ensure ports are free
lsof -ti :8090 | xargs kill -9 2>/dev/null || true

# Helper to init dirs since setup_workspace only does mixer/rack
mkdir -p $RACK_A_DIR/data $RACK_A_DIR/logs
mkdir -p $RACK_B_DIR/data $RACK_B_DIR/logs
mkdir -p $RACK_C_DIR/data $RACK_C_DIR/logs
mkdir -p $RACK_D_DIR/data $RACK_D_DIR/logs
mkdir -p $RACK_E_DIR/data $RACK_E_DIR/logs

# 1. Start Mixer
log_info "Starting Mixer..."
"${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "$MIXER_DIR/data/cluster.key" > /dev/null
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"

# The Mixer now logs to a file by default for coexistence. 
# We wait for the pattern in both stdout and the log file.
MIXER_FILE_LOG="$MIXER_DIR/logs/mixer.log"
mkdir -p "$(dirname "$MIXER_FILE_LOG")"
touch "$MIXER_FILE_LOG"

wait_for_log_dual() {
    local file1=$1
    local file2=$2
    local pattern=$3
    local timeout=$4
    local name=$5
    
    log_info "Waiting for '$name' ($pattern) in logs..."
    local start_time=$(date +%s)
    while ! grep -q "$pattern" "$file1" 2>/dev/null && ! grep -q "$pattern" "$file2" 2>/dev/null; do
        if [ $(($(date +%s) - start_time)) -gt $timeout ]; then
            fail "Timeout waiting for $name after ${timeout}s"
        fi
        sleep 0.2
    done
}

wait_for_log_dual "$MIXER_LOG" "$MIXER_FILE_LOG" "Mixer is ready" 15 "Mixer Startup"

# Helper to purge registry by name to ensure Clean Registry for tests
purge_rack() {
    local name=$1
    log_info "Purging stale registry for '$name'..."
    curl -s -X DELETE "$API_URL/racks/$name" > /dev/null || true
}

# ==========================================
# Scenario 1: Active Conflict (Security)
# ==========================================
log_info "--- Scenario 1: Active Conflict ---"

purge_rack "rack-shared"
log_info "Starting Rack A (Legitimate Owner)..."
cp "${BASE_DIR}/rack_a/rack.toml" "${WORK_DIR}/rack_a/rack.toml"
cd "${WORK_DIR}/rack_a"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_A_PID=$!
cd "${BASE_DIR}"

# Regex for UUID
UUID_PATTERN="[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"

wait_for_log "$RACK_A_DIR/rack.stdout" "Passport Verified" 20 "Rack A Enrollment"
ID_A=$(grep -a "Passport Verified" "$RACK_A_DIR/rack.stdout" | grep -oE "id=$UUID_PATTERN" | head -n1 | cut -d= -f2)
NAME_A=$(grep -a "Passport Verified" "$RACK_A_DIR/rack.stdout" | grep -oE "name=[^ ]*" | head -n1 | cut -d= -f2)
log_success "Rack A Registered (ID: $ID_A, Name: $NAME_A)"

log_info "Starting Rack B (Hijacker - No Secret)..."
cp "${BASE_DIR}/rack_b/rack.toml" "${WORK_DIR}/rack_b/rack.toml"
cd "${WORK_DIR}/rack_b"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_B_PID=$!
cd "${BASE_DIR}"

log_info "Waiting to ensure Rack B is rejected..."
sleep 3
if grep -q "Passport Verified" "$RACK_B_DIR/rack.stdout"; then
    fail "Rack B registered successfully (Should be Rejected)"
fi
log_success "Rack B was rejected (No Passport issued)"
kill -9 $RACK_B_PID 2>/dev/null || true

# ==========================================
# Scenario 2: Session Recovery
# ==========================================
log_info "--- Scenario 2: Session Recovery ---"

log_info "Killing Rack A..."
kill -9 $RACK_A_PID 2>/dev/null || true
wait $RACK_A_PID 2>/dev/null

log_info "Restarting Rack A (With Passport/Secret)..."
cd "${WORK_DIR}/rack_a"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" >> "rack.stdout" 2>&1 &
RACK_A_PID=$!
cd "${BASE_DIR}"

wait_for_log "$RACK_A_DIR/rack.stdout" "Loaded Cached Passport" 20 "Rack A Recovery"
log_success "Rack A recovered session"

# ==========================================
# Scenario 3: Zero Config (Auto-Scale)
# ==========================================
log_info "--- Scenario 3: Zero-Config (Cattle) ---"

log_info "Starting Rack C..."
cp "${BASE_DIR}/rack_c/rack.toml" "${WORK_DIR}/rack_c/rack.toml"
cd "${WORK_DIR}/rack_c"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_C_PID=$!
cd "${BASE_DIR}"

wait_for_log "$RACK_C_DIR/rack.stdout" "Passport Verified" 20 "Rack C Enrollment"

ID_C=$(grep "Passport Verified" "$RACK_C_DIR/rack.stdout" | grep -oE "id=$UUID_PATTERN" | head -n1 | cut -d= -f2)
NAME_C=$(grep "Passport Verified" "$RACK_C_DIR/rack.stdout" | grep -oE 'name=[^ ]*' | head -n1 | cut -d= -f2)
if [ -z "$ID_C" ]; then
    fail "Rack C failed to register"
fi

if [[ "$NAME_C" != probes-* ]]; then
    fail "Rack C assigned name '$NAME_C' does not start with 'probes-'"
fi
log_success "Rack C Registered (ID: $ID_C, Name: $NAME_C)"

log_info "Starting Rack D..."
cp "${BASE_DIR}/rack_d/rack.toml" "${WORK_DIR}/rack_d/rack.toml"
cd "${WORK_DIR}/rack_d"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_D_PID=$!
cd "${BASE_DIR}"

wait_for_log "$RACK_D_DIR/rack.stdout" "Passport Verified" 20 "Rack D Enrollment"

ID_D=$(grep "Passport Verified" "$RACK_D_DIR/rack.stdout" | grep -oE "id=$UUID_PATTERN" | head -n1 | cut -d= -f2)
NAME_D=$(grep "Passport Verified" "$RACK_D_DIR/rack.stdout" | grep -oE 'name=[^ ]*' | head -n1 | cut -d= -f2)
if [ -z "$ID_D" ]; then
    fail "Rack D failed to register"
fi

if [[ "$NAME_D" != probes-* ]]; then
    fail "Rack D assigned name '$NAME_D' does not start with 'probes-'"
fi
log_success "Rack D Registered (ID: $ID_D, Name: $NAME_D)"

if [ "$ID_C" == "$ID_D" ]; then
    fail "Rack C and D got same ID ($ID_C)"
fi
log_success "Unique IDs assigned via Auto-Scale"

# ==========================================
# Scenario 4: Default Zero Config (No Prefix)
# ==========================================
log_info "--- Scenario 4: Default Zero Config ---"

log_info "Starting Rack E (No Prefix)..."
cp "${BASE_DIR}/rack_e/rack.toml" "${WORK_DIR}/rack_e/rack.toml"
cd "${WORK_DIR}/rack_e"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_E_PID=$!
cd "${BASE_DIR}"

wait_for_log "$RACK_E_DIR/rack.stdout" "Passport Verified" 20 "Rack E Enrollment"

ID_E=$(grep "Passport Verified" "$RACK_E_DIR/rack.stdout" | grep -oE "id=$UUID_PATTERN" | head -n1 | cut -d= -f2)
NAME_E=$(grep "Passport Verified" "$RACK_E_DIR/rack.stdout" | grep -oE 'name=[^ ]*' | head -n1 | cut -d= -f2)
if [ -z "$ID_E" ]; then
    fail "Rack E failed to register"
fi

if [[ "$NAME_E" != node-* ]]; then
    fail "Rack E assigned name '$NAME_E' does not start with default 'node-'"
fi
log_success "Rack E Registered (ID: $ID_E, Name: $NAME_E)"

banner "ALL SCENARIOS PASSED"
exit 0
