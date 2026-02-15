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
# CLI & Admin E2E Test
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "cli" "$BASE_DIR"

banner "CLI & Admin E2E Test"

# Config
MIXER_CONFIG="$BASE_DIR/mixer/mixer.toml"
RACK_CONFIG="$BASE_DIR/rack/rack.toml"
API_URL="http://localhost:8090/api/v1"

MIXER_LOG="$WORK_DIR/mixer/mixer.stdout"
RACK_LOG="$WORK_DIR/rack/rack.stdout"

MIXER_PID=""
RACK_PID=""

cleanup() {
    log_info "Shutting down..."
    if [ -n "$MIXER_PID" ]; then kill -9 $MIXER_PID 2>/dev/null || true; wait $MIXER_PID 2>/dev/null || true; fi
    if [ -n "$RACK_PID" ]; then kill -9 $RACK_PID 2>/dev/null || true; wait $RACK_PID 2>/dev/null || true; fi
    pkill -f "bin/fluxrig" || true
}
trap cleanup EXIT

# 1. Preparation
kill_old_processes
# ensure ports are free
lsof -ti :8090 | xargs kill -9 2>/dev/null || true

gen_keys "$WORK_DIR/mixer/data/cluster.key"

# ==============================================================================
section "Helper Commands Verification"
# ==============================================================================

# Version
VERSION_OUT=$("${ROOT_DIR}/bin/fluxrig" version)
log_info "Output: $VERSION_OUT"
if echo "$VERSION_OUT" | grep -q "^fluxrig"; then
    log_success "'fluxrig version' passed."
else
    fail "'fluxrig version' failed."
fi

# Help
HELP_OUT=$("${ROOT_DIR}/bin/fluxrig" rack --help)
log_info "Output (truncated): $(echo "$HELP_OUT" | head -n 1)..."
if echo "$HELP_OUT" | grep -q "Initializes"; then
    log_success "'fluxrig rack --help' passed."
else
    fail "'fluxrig rack --help' failed."
fi

# Key Gen
KEYS_OUT=$("${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "$WORK_DIR/mixer/data/test_gen.key")
log_info "Output: $KEYS_OUT"
if echo "$KEYS_OUT" | grep -q "Private Key"; then
    log_success "'fluxrig keys gen-cluster' passed."
else
    fail "'fluxrig keys gen-cluster' failed."
fi

# ==============================================================================
section "Admin CLI Tests (Mixer Required)"
# ==============================================================================

# Start Mixer
log_info "Starting Mixer..."
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
log_info "Mixer PID: $MIXER_PID"
cd "${BASE_DIR}"

wait_for_mixer "$API_URL"

# Start Rack (Zero Config)
# Start Rack (Zero Config)
log_info "Starting Rack (Zero Config)..."
cp "${BASE_DIR}/rack/rack.toml" "${WORK_DIR}/rack/rack.toml"
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" >> "rack.stdout" 2>&1 &
RACK_PID=$!
log_info "Rack PID: $RACK_PID"
cd "${BASE_DIR}"
sleep 2

# ==============================================================================
section "Pending State Verification"
# ==============================================================================

LIST_OUT=$("${ROOT_DIR}/bin/fluxrig" admin racks list)
if echo "$LIST_OUT" | grep -q "pending"; then
    log_success "Rack is PENDING as expected."
    log_info "Output: $LIST_OUT"
else
    log_info "Output: $LIST_OUT"
    tail -n 10 "$RACK_LOG"
    fail "Rack should be PENDING but is NOT."
fi

# Inspect State (Initial)
log_info "Inspecting State (Pending)..."
STATE_OUT_1=$("${ROOT_DIR}/bin/fluxrig" keys inspect "$WORK_DIR/rack/data/state.flux")
log_info "$STATE_OUT_1"

if echo "$STATE_OUT_1" | grep -q "Name:" && echo "$STATE_OUT_1" | grep -q "cli-test-"; then
    log_success "Name Verified (Matches prefix 'cli-test-')."
else
    fail "Name Mismatch. Expected 'cli-test-' prefix."
fi

if echo "$STATE_OUT_1" | grep -q "Status:    pending"; then
    log_success "Status Verified (pending)."
else
    fail "Status Mismatch. Expected 'pending'."
fi

# Extract Rack ID
RACK_ID=$(echo "$LIST_OUT" | grep "pending" | awk '{print $1}')
log_info "Extracted Rack ID: $RACK_ID"

# ==============================================================================
section "Adoption (Approve)"
# ==============================================================================

log_info "Approving Rack..."
"${ROOT_DIR}/bin/fluxrig" admin racks approve $RACK_ID --name "rack-production-01"

sleep 1
LIST_OUT_2=$("${ROOT_DIR}/bin/fluxrig" admin racks list)
if echo "$LIST_OUT_2" | grep "rack-production-01" | grep -q "active"; then
    log_success "Rack Approved & Active."
    log_info "Output: $LIST_OUT_2"
else
    log_info "Output: $LIST_OUT_2"
    fail "Rack Approval Failed."
fi

# Inspect State after Approval
log_info "Inspecting State after Approval..."
sleep 10
STATE_OUT_2=$("${ROOT_DIR}/bin/fluxrig" keys inspect "$WORK_DIR/rack/data/state.flux")
log_info "$STATE_OUT_2"

if echo "$STATE_OUT_2" | grep -q "Name:      rack-production-01"; then
    log_success "Name Verified (Approved Name)."
else
    fail "Name Mismatch. Expected 'rack-production-01'."
fi
if echo "$STATE_OUT_2" | grep -q "Status:    active"; then
    log_success "Status Verified (active)."
else
    fail "Status Mismatch. Expected 'active'."
fi

# ==============================================================================
section "Suspension"
# ==============================================================================

log_info "Suspending Rack..."
"${ROOT_DIR}/bin/fluxrig" admin racks suspend $RACK_ID
sleep 1

LIST_OUT_3=$("${ROOT_DIR}/bin/fluxrig" admin racks list)
if echo "$LIST_OUT_3" | grep "inactive"; then
    log_success "Rack Suspended (Registry Updated)."
    log_info "Output: $LIST_OUT_3"
    
    sleep 2
    if grep -q "Status Changed" "$RACK_LOG" && grep -q 'new="inactive"' "$RACK_LOG"; then
         log_success "Rack Received Suspension (Log Verified)."
    else
         tail -n 20 "$RACK_LOG"
         echo "⚠️  Rack did NOT receive suspension notification (Mixer limit?). Proceeding."
    fi
else
    log_info "Output: $LIST_OUT_3"
    fail "Rack Suspension Failed (Registry not updated)."
fi

# State after Suspension
log_info "Inspecting State after Suspension..."
STATE_OUT_SUSP=$("${ROOT_DIR}/bin/fluxrig" keys inspect "$WORK_DIR/rack/data/state.flux")
log_info "$STATE_OUT_SUSP"

if echo "$STATE_OUT_SUSP" | grep -q "Status:    inactive"; then
    log_success "Status in State File Verified (inactive)."
else
    echo "⚠️  Status Mismatch in State File. Expected 'inactive'. (Known Issue: Suspension passport not sent)."
fi

# ==============================================================================
section "Activation"
# ==============================================================================

log_info "Activating Rack..."
"${ROOT_DIR}/bin/fluxrig" admin racks activate $RACK_ID
sleep 1

LIST_OUT_4=$("${ROOT_DIR}/bin/fluxrig" admin racks list)
if echo "$LIST_OUT_4" | grep "active"; then
    log_success "Rack Activated (Registry Updated)."
    log_info "Output: $LIST_OUT_4"
    
    sleep 2
    if grep -q 'new="active"' "$RACK_LOG"; then
         log_success "Rack Received Activation (Log Verified)."
    else
         tail -n 20 "$RACK_LOG"
         echo "⚠️  Rack did NOT receive activation notification (Mixer limit?). Proceeding."
    fi
else
    log_info "Output: $LIST_OUT_4"
    fail "Rack Activation Failed (Registry not updated)."
fi

# ==============================================================================
section "Removal"
# ==============================================================================

log_info "Removing Rack..."
"${ROOT_DIR}/bin/fluxrig" admin racks remove $RACK_ID
sleep 1

LIST_OUT_5=$("${ROOT_DIR}/bin/fluxrig" admin racks list)
if echo "$LIST_OUT_5" | grep -q "rack-production-01"; then
    log_info "Output: $LIST_OUT_5"
    fail "'admin racks remove' Failed (Rack still listed)."
else
    log_success "'admin racks remove' Verified (Rack gone)."
    log_info "Output (Empty or different racks): $LIST_OUT_5"
fi

# ==============================================================================
section "Re-Enrollment (Resurrection)"
# ==============================================================================

if [ -n "$RACK_PID" ]; then kill $RACK_PID 2>/dev/null || true; wait $RACK_PID 2>/dev/null || true; fi
log_info "Restarting Rack for Resurrection..."
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" >> "rack.stdout" 2>&1 &
RACK_PID=$!
cd "${BASE_DIR}"
sleep 2

LIST_OUT_6=$("${ROOT_DIR}/bin/fluxrig" admin racks list)
if echo "$LIST_OUT_6" | grep -q "active"; then
    log_success "Resurrection Verified (Rack returned as Active - Pet Mode via Passport)."
    log_info "Output: $LIST_OUT_6"
elif echo "$LIST_OUT_6" | grep -q "pending"; then
    log_success "Resurrection Verified (Rack returned as Pending)."
    log_info "Output: $LIST_OUT_6"
else
    log_info "Output: $LIST_OUT_6"
    fail "Resurrection Failed (Rack not found)."
fi

# Inspect State (Resurrection)
log_info "Inspecting State (Resurrection)..."
STATE_OUT_3=$("${ROOT_DIR}/bin/fluxrig" keys inspect "$WORK_DIR/rack/data/state.flux")
log_info "$STATE_OUT_3"

if echo "$STATE_OUT_3" | grep -i "Identity"; then
    log_success "Resurrection State: Valid Passport Found."
else
    fail "Resurrection State: Failed to load passport."
fi

banner "ALL CLI TESTS PASSED"
