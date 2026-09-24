#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Hot Lane E2E Test
#
# A wire between two gears of one Rack goes through the Rack's memory. The Mixer's
# store is left in clear on purpose, as the detector: a message that reached the
# Mixer would be readable in it.
#
#   1. The echo works and nothing of it reaches the Mixer.
#   2. The Rack keeps serving the wire while the Mixer is dead.
#   3. Control: the same wire on the guaranteed lane does leave its message on the
#      Mixer, in clear here, so the checks above are able to fail.
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "hot_lane" "$BASE_DIR"

banner "Hot Lane E2E Test"

API_URL="http://localhost:8090/api/v1"
HOT_PORT=9611
GUARANTEED_PORT=9612
PAN=4111111111111111
MESSAGES=20
STREAM_DIR="$WORK_DIR/mixer/data/snake/jetstream"

# Only the message stream: the telemetry stream carries the Rack's log lines, which
# name the subject of every wire.
msg_stream_files() {
    find "$STREAM_DIR" -path '*/streams/flux-msg/*' -type f -print0 2>/dev/null
}

ENROLL_TIMEOUT=30
PORT_TIMEOUT=30
RECOVER_TIMEOUT=60

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
    cd "$WORK_DIR/mixer"
    "$(bin_dir)/fluxrig-mixer" -c mixer.toml >> mixer.stdout 2>&1 &
    MIXER_PID=$!
    cd "${BASE_DIR}"
    wait_for_port 8090 30 || fail "Mixer API did not come up."
}

# echo_line PORT TEXT: sends a line and prints what comes back
echo_line() {
    local port="$1" text="$2" line=""
    if exec 3<>/dev/tcp/127.0.0.1/"$port" 2>/dev/null; then
        printf '%s\n' "$text" >&3
        read -t 3 -u 3 line 2>/dev/null
        exec 3>&- 3<&-
    fi
    printf '%s' "$line"
}

# echoes PORT PREFIX: how many of MESSAGES lines carrying a card number came back
echoes() {
    local port="$1" prefix="$2" ok=0 i text
    for i in $(seq 1 $MESSAGES); do
        text=$(printf '%s%s%03d' "$prefix" "$PAN" "$i")
        [ "$(echo_line "$port" "$text")" == "$text" ] && ok=$((ok+1))
    done
    echo "$ok"
}

import_scenario() {
    local code
    code=$(curl -s -o "$WORK_DIR/import.out" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
        -H "Content-Type: application/x-yaml" --data-binary @"$1")
    [ "$code" == "200" ] || { cat "$WORK_DIR/import.out"; fail "Scenario import failed (HTTP $code)."; }
}

wait_rack_active() {
    local deadline=$((SECONDS + ENROLL_TIMEOUT))
    until curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-hot"; do
        (( SECONDS < deadline )) || fail "rack-hot did not become active."
        sleep 0.5
    done
}

# 0. Prep
lsof -ti :8090 | xargs kill -9 2>/dev/null || true

# 1. Mixer and Rack
"$(bin_dir)/fluxrig" keys gen-cluster -o "$WORK_DIR/mixer/data/cluster.key" > /dev/null
mkdir -p "$WORK_DIR/rack"
cp "${BASE_DIR}/mixer/mixer.toml" "$WORK_DIR/mixer/mixer.toml"
cp "${BASE_DIR}/rack/rack.toml" "$WORK_DIR/rack/rack.toml"

start_mixer_here
cd "$WORK_DIR/rack"
"$(bin_dir)/fluxrig" rack -c rack.toml >> rack.stdout 2>&1 &
RACK_PID=$!
cd "${BASE_DIR}"
wait_rack_active
log_success "Rack rack-hot is active."

# 2. The hot lane: the echo works and nothing of it reaches the Mixer
log_info "--- Phase 1: a wire inside one Rack (${SECONDS}s) ---"
import_scenario "${BASE_DIR}/scenario_hot.yaml"
wait_for_port $HOT_PORT "$PORT_TIMEOUT" || fail "The gateway is not listening."

OK=$(echoes $HOT_PORT HOT)
[ "$OK" == "$MESSAGES" ] || fail "Only $OK of $MESSAGES lines came back through the hot lane."
log_success "$OK of $MESSAGES lines came back (the wire works)."
sleep 2

if grep -rqaE "$PAN" "$WORK_DIR/mixer" 2>/dev/null; then
    grep -rlaE "$PAN" "$WORK_DIR/mixer" | sed "s#$WORK_DIR/##"
    fail "The card number reached the Mixer."
fi
log_success "The card number is nowhere on the Mixer, in a store that is not encrypted."

if msg_stream_files | xargs -0 -r grep -qaF "flux.msg.rack-hot.gateway.out"; then
    fail "A message on the hot wire reached the Mixer's stream."
fi
log_success "The Mixer's stream holds nothing from the hot wire."

# 3. The Rack keeps serving the wire while the Mixer is dead
log_info "--- Phase 2: the Mixer dies (${SECONDS}s) ---"
stop_pid "$MIXER_PID"; MIXER_PID=""
sleep 1

OK=$(echoes $HOT_PORT DEAD)
[ "$OK" == "$MESSAGES" ] || fail "Only $OK of $MESSAGES lines came back while the Mixer was down."
log_success "The Rack served $OK of $MESSAGES lines with the Mixer dead."

start_mixer_here
deadline=$((SECONDS + RECOVER_TIMEOUT))
until curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-hot"; do
    (( SECONDS < deadline )) || fail "The Rack did not come back to the Mixer."
    sleep 0.5
done
log_success "The Rack found the Mixer again (${SECONDS}s)."

# 4. Control: the same wire on the guaranteed lane leaves its message on the Mixer
log_info "--- Phase 3: control, the guaranteed lane (${SECONDS}s) ---"
import_scenario "${BASE_DIR}/scenario_guaranteed.yaml"
wait_for_port $GUARANTEED_PORT "$PORT_TIMEOUT" || fail "The guaranteed gateway is not listening."

OK=$(echoes $GUARANTEED_PORT GUAR)
[ "$OK" == "$MESSAGES" ] || fail "Only $OK of $MESSAGES lines came back through the guaranteed lane."
sleep 2

COPIES=$(msg_stream_files | xargs -0 -r grep -haoE "GUAR$PAN[0-9]{3}" | wc -l)
if [ "$COPIES" -ge "$MESSAGES" ]; then
    log_success "Control: the guaranteed wire left $COPIES readable copies on the Mixer, so the checks above can fail."
else
    fail "Control failed: only $COPIES copies of the guaranteed messages were found on the Mixer."
fi

banner "Hot Lane Verified"
