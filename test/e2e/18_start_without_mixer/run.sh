#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Start Without the Mixer E2E Test
#
# A Rack whose scenario keeps every wire inside it starts, and serves traffic, while
# the Mixer is away. When the Mixer returns the Rack joins it without stopping what it
# is serving: a client that was connected before stays connected and keeps being
# answered, and the gears are not started again.
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "start_without_mixer" "$BASE_DIR"

banner "Start Without the Mixer E2E Test"

API_URL="http://localhost:8090/api/v1"
GATEWAY_PORT=9631
RACK_STDOUT="$WORK_DIR/rack/rack.stdout"

ENROLL_TIMEOUT=30
START_TIMEOUT=30
REJOIN_TIMEOUT=60

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
    exec 5>&- 5<&- 2>/dev/null || true
    stop_pid "$MIXER_PID"
    stop_pid "$RACK_PID"
}
trap cleanup EXIT

start_mixer_here() {
    cd "${WORK_DIR}/mixer"
    "$(bin_dir)/fluxrig-mixer" -c "mixer.toml" >> mixer.stdout 2>&1 &
    MIXER_PID=$!
    cd "${BASE_DIR}"
    wait_for_port 8090 30 || fail "Mixer API did not come up."
}

start_rack_here() {
    cd "${WORK_DIR}/rack"
    "$(bin_dir)/fluxrig" rack -c "rack.toml" >> rack.stdout 2>&1 &
    RACK_PID=$!
    cd "${BASE_DIR}"
}

# echo_line PORT TEXT: a new connection, one line, and what comes back
echo_line() {
    local port="$1" text="$2" line=""
    if exec 3<>/dev/tcp/127.0.0.1/"$port" 2>/dev/null; then
        printf '%s\n' "$text" >&3
        read -t 3 -u 3 line 2>/dev/null
        exec 3>&- 3<&-
    fi
    printf '%s' "$line"
}

# How many times the gateway gear was started, from the Rack's log. The log repeats an
# earlier event once telemetry starts, with the same timestamp, so the starts are
# counted by their distinct timestamps.
gateway_starts() {
    grep -a 'gear started.*gateway' "$RACK_STDOUT" | awk -F'|' '{print $1}' | sort -u | wc -l
}

# 0. Prep
lsof -ti :8090 | xargs kill -9 2>/dev/null || true
lsof -ti :$GATEWAY_PORT | xargs kill -9 2>/dev/null || true

# 1. Enroll with the Mixer up, apply the scenario, and let the Rack keep its copy
log_info "--- Phase 1: enroll and apply a scenario ---"
"$(bin_dir)/fluxrig" keys gen-cluster -o "$WORK_DIR/mixer/data/cluster.key" > /dev/null
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
cp "${BASE_DIR}/rack/rack.toml" "${WORK_DIR}/rack/rack.toml"

start_mixer_here
start_rack_here

deadline=$((SECONDS + ENROLL_TIMEOUT))
until curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-solo"; do
    (( SECONDS < deadline )) || { cat "$RACK_STDOUT"; fail "rack-solo did not become active."; }
    sleep 0.5
done

HTTP_CODE=$(curl -s -o "$WORK_DIR/import.out" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
    -H "Content-Type: application/x-yaml" --data-binary @"${BASE_DIR}/scenario.yaml")
[ "$HTTP_CODE" == "200" ] || { cat "$WORK_DIR/import.out"; fail "Scenario import failed (HTTP $HTTP_CODE)."; }
wait_for_port $GATEWAY_PORT "$START_TIMEOUT" || fail "The scenario never started."
wait_for_file "$WORK_DIR/rack/data/scenario.flux" 15 || fail "The Rack kept no copy of the scenario."
log_success "The scenario runs and the Rack kept its copy."

# 2. Both go down. The Rack starts alone.
log_info "--- Phase 2: the Rack starts while the Mixer is away ---"
stop_pid "$RACK_PID"; RACK_PID=""
stop_pid "$MIXER_PID"; MIXER_PID=""
lsof -Pi :$GATEWAY_PORT -sTCP:LISTEN -t >/dev/null 2>&1 && fail "The gateway kept listening after the Rack stopped."

SEEN=$(wc -l < "$RACK_STDOUT")
start_rack_here

if wait_for_log "$RACK_STDOUT" "Resumed last scenario from local state" "$START_TIMEOUT" "$SEEN"; then
    log_success "(1/3) The Rack started its saved scenario with no Mixer."
else
    cat "$RACK_STDOUT"
    fail "(1/3) The Rack did not start its saved scenario without the Mixer."
fi
wait_for_port $GATEWAY_PORT "$START_TIMEOUT" || fail "(2/3) The gateway is not listening without the Mixer."

REPLY=$(echo_line $GATEWAY_PORT "alone-1")
[ "$REPLY" == "alone-1" ] || fail "(2/3) The gateway did not answer without the Mixer (got '$REPLY')."
log_success "(2/3) The gateway answers with no Mixer."

if tail -n +$((SEEN + 1)) "$RACK_STDOUT" | grep -qE 'panic|nil pointer'; then
    tail -n +$((SEEN + 1)) "$RACK_STDOUT" | grep -E 'panic|nil pointer' | head -3
    fail "(3/3) The Rack panicked starting without the Mixer."
fi
log_success "(3/3) No panic."

# 3. A client is connected, and the Mixer returns
log_info "--- Phase 3: the Mixer returns while a client is connected ---"
exec 5<>/dev/tcp/127.0.0.1/$GATEWAY_PORT || fail "Could not open the client connection."
printf 'before\n' >&5
read -t 3 -u 5 LINE
[ "$LINE" == "before" ] || fail "The open connection did not echo before the Mixer returned (got '$LINE')."
log_success "A client is connected and answered."

STARTS_BEFORE=$(gateway_starts)
SEEN=$(wc -l < "$RACK_STDOUT")
start_mixer_here

if wait_for_log "$RACK_STDOUT" "Keeping the gears the previous session left running" "$REJOIN_TIMEOUT" "$SEEN"; then
    log_success "(1/4) The Rack joined the Mixer and kept its gears."
else
    cat "$RACK_STDOUT"
    fail "(1/4) The Rack did not join the Mixer."
fi

deadline=$((SECONDS + REJOIN_TIMEOUT))
until curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-solo"; do
    (( SECONDS < deadline )) || fail "(2/4) The Mixer never listed the Rack as active."
    sleep 0.5
done
log_success "(2/4) The Mixer lists the Rack as active."

# The client that was connected before is still connected, and answered.
printf 'after\n' >&5
read -t 3 -u 5 LINE
[ "$LINE" == "after" ] || fail "(3/4) The connection that was open did not survive the rejoin (got '$LINE')."
log_success "(3/4) The client that was connected stayed connected and was answered."

# Joining the Mixer started nothing again.
STARTS_AFTER=$(gateway_starts)
[ "$STARTS_AFTER" == "$STARTS_BEFORE" ] || fail "(4/4) The gateway was started again: $STARTS_BEFORE starts before, $STARTS_AFTER after."
log_success "(4/4) Joining the Mixer did not start the gateway again."

REPLY=$(echo_line $GATEWAY_PORT "new-connection")
[ "$REPLY" == "new-connection" ] || fail "A new connection was not answered after the rejoin."
log_success "A new connection is answered as well."

# 4. The Rack that took over its gears takes a new scenario from the Mixer like any
# other. Activating a scenario gives its entities new identifiers, so the Rack treats it
# as a new deployment: the gateway is restarted, and answers again.
log_info "--- Phase 4: the Mixer deploys a scenario to the Rack that kept its gears ---"
SEEN=$(wc -l < "$RACK_STDOUT")
HTTP_CODE=$(curl -s -o "$WORK_DIR/import.out" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
    -H "Content-Type: application/x-yaml" --data-binary @"${BASE_DIR}/scenario.yaml")
[ "$HTTP_CODE" == "200" ] || { cat "$WORK_DIR/import.out"; fail "Activating the scenario failed (HTTP $HTTP_CODE)."; }

wait_for_log "$RACK_STDOUT" "Scenario Applied Successfully" "$REJOIN_TIMEOUT" "$SEEN" || { cat "$RACK_STDOUT" | tail -20; fail "The Rack did not apply the Mixer's scenario."; }
wait_for_port $GATEWAY_PORT "$START_TIMEOUT" || fail "The gateway is not listening after the Mixer's scenario."
REPLY=$(echo_line $GATEWAY_PORT "deployed")
[ "$REPLY" == "deployed" ] || fail "The gateway did not answer after the Mixer's scenario (got '$REPLY')."
log_success "The Mixer's scenario was applied and the gateway answers."

banner "Start Without the Mixer Verified"
