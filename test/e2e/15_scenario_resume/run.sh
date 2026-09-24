#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Scenario Resume E2E Test
#
# A Rack keeps its own copy of the last scenario it applied and resumes it at
# start, without waiting for the Mixer to send it again. A TCP gear listening on
# a port is the evidence that the scenario is running.
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "scenario_resume" "$BASE_DIR"

banner "Scenario Resume E2E Test"

API_URL="http://localhost:8090/api/v1"
GATEWAY_PORT=9611
SAVED_COPY="$WORK_DIR/rack/data/scenario.flux"
RACK_STDOUT="$WORK_DIR/rack/rack.stdout"

# Deadlines for the polling waits
ENROLL_TIMEOUT=30
RESUME_TIMEOUT=30
PUSH_TIMEOUT=30

MIXER_PID=""
RACK_PID=""

stop_pid() {
    local pid="$1"
    if [ -n "$pid" ]; then
        kill -9 "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    fi
}

cleanup() {
    log_info "Shutting down..."
    stop_pid "$MIXER_PID"
    stop_pid "$RACK_PID"
}
trap cleanup EXIT

start_mixer_here() {
    cd "${WORK_DIR}/mixer"
    "$(bin_dir)/fluxrig-mixer" -c "mixer.toml" >> "mixer.stdout" 2>&1 &
    MIXER_PID=$!
    cd "${BASE_DIR}"
    wait_for_port 8090 30 || fail "Mixer API did not come up."
}

start_rack_here() {
    cd "${WORK_DIR}/rack"
    "$(bin_dir)/fluxrig" rack -c "rack.toml" >> "rack.stdout" 2>&1 &
    RACK_PID=$!
    cd "${BASE_DIR}"
}

port_is_listening() {
    lsof -Pi :"$1" -sTCP:LISTEN -t >/dev/null 2>&1
}

# 0. Prep
lsof -ti :8090 | xargs kill -9 2>/dev/null || true
lsof -ti :$GATEWAY_PORT | xargs kill -9 2>/dev/null || true

# 1. Mixer and Rack, online
log_info "--- Phase 1: Enroll and apply a scenario ---"
"$(bin_dir)/fluxrig" keys gen-cluster -o "$WORK_DIR/mixer/data/cluster.key" > /dev/null
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
cp "${BASE_DIR}/rack/rack.toml" "${WORK_DIR}/rack/rack.toml"

start_mixer_here
start_rack_here

deadline=$((SECONDS + ENROLL_TIMEOUT))
until curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-resume"; do
    (( SECONDS < deadline )) || { cat "$RACK_STDOUT"; fail "rack-resume did not become active."; }
    sleep 0.5
done
log_success "Rack rack-resume is active."

HTTP_CODE=$(curl -s -o "$WORK_DIR/import.out" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
    -H "Content-Type: application/x-yaml" --data-binary @"${BASE_DIR}/scenario.yaml")
if [ "$HTTP_CODE" != "200" ]; then
    cat "$WORK_DIR/import.out"
    fail "Scenario import and activation failed (HTTP $HTTP_CODE)."
fi

wait_for_port $GATEWAY_PORT "$RESUME_TIMEOUT" || { cat "$RACK_STDOUT"; fail "The scenario never started on the Rack."; }
log_success "Scenario is running (gateway listening on $GATEWAY_PORT)."

if wait_for_file "$SAVED_COPY" 15; then
    log_success "The Rack kept a local copy of the scenario."
else
    fail "No local copy of the scenario at $SAVED_COPY."
fi

# 2. The Rack restarts while the Mixer is up
log_info "--- Phase 2: Rack restarts, Mixer up ---"
stop_pid "$RACK_PID"; RACK_PID=""
port_is_listening $GATEWAY_PORT && fail "The gateway kept listening after the Rack stopped."
log_success "Rack stopped; the gateway is down."

SEEN=$(wc -l < "$RACK_STDOUT")
start_rack_here

if wait_for_log "$RACK_STDOUT" "Resumed last scenario from local state" "$RESUME_TIMEOUT" "$SEEN"; then
    log_success "(1/2) Rack resumed its last scenario from local state."
else
    cat "$RACK_STDOUT"
    fail "(1/2) Rack did not resume its last scenario."
fi
wait_for_port $GATEWAY_PORT "$RESUME_TIMEOUT" || fail "(2/2) The gateway is not listening after the resume."
log_success "(2/2) The scenario is running again."

# The Mixer sends its own copy of the scenario a moment after the Rack says hello.
# It is the same one, so the gears must keep running rather than restart.
if wait_for_log "$RACK_STDOUT" "matches the one resumed from local state" "$PUSH_TIMEOUT" "$SEEN"; then
    log_success "The Mixer's scenario matched the resumed one; the gears were left running."
else
    cat "$RACK_STDOUT"
    fail "The Mixer's scenario did not arrive, or was not recognised as the resumed one."
fi
port_is_listening $GATEWAY_PORT || fail "The gateway stopped when the Mixer's scenario arrived."
log_success "The gateway is still listening."

# 3. The Rack restarts while the Mixer is down, and the Mixer returns
log_info "--- Phase 3: Rack restarts, Mixer down, then back ---"
stop_pid "$RACK_PID"; RACK_PID=""
stop_pid "$MIXER_PID"; MIXER_PID=""
port_is_listening $GATEWAY_PORT && fail "The gateway kept listening after the Rack stopped."

SEEN=$(wc -l < "$RACK_STDOUT")
start_rack_here

# The scenario keeps every wire inside the Rack, so it needs no bus: the Rack starts it
# on its own, and serves it while the Mixer is away.
if wait_for_log "$RACK_STDOUT" "Resumed last scenario from local state" "$RESUME_TIMEOUT" "$SEEN"; then
    log_success "(1/3) Rack started offline and ran its saved scenario without the Mixer."
else
    cat "$RACK_STDOUT"
    fail "(1/3) Rack did not start its saved scenario without the Mixer."
fi
wait_for_port $GATEWAY_PORT "$RESUME_TIMEOUT" || fail "(1/3) The gateway is not listening without the Mixer."

SEEN=$(wc -l < "$RACK_STDOUT")
start_mixer_here

if wait_for_log "$RACK_STDOUT" "Keeping the gears the previous session left running" "$RESUME_TIMEOUT" "$SEEN"; then
    log_success "(2/3) Rack joined the Mixer and kept its gears."
else
    cat "$RACK_STDOUT"
    fail "(2/3) Rack did not join the Mixer after it returned."
fi
port_is_listening $GATEWAY_PORT || fail "(3/3) The gateway is not listening after the Mixer returned."
log_success "(3/3) The scenario is still running."

banner "Scenario Resume Verified"
