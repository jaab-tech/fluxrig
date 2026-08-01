#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
#
# End-to-end proof of the processor simulator: a fluxrig Rack (io server ->
# codec decode -> bento reply builder -> codec encode -> loopback) that answers
# an ISO 8583 request with an authored 0810 approval. A terminal (the Go load
# tool) sends requests to the sim and must receive correlated responses,
# proving the full path over real sockets with the real bento gear.
set -uo pipefail

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
SUITE_ROOT="$(cd "${BASE_DIR}/../../.." && pwd)"    # repo root (3 up from test/e2e/13_*)
source "${SUITE_ROOT}/test/e2e/utils/e2e_utils.sh"
ensure_root

MIXER_API_PORT=9130
SNAKE_PORT=4243
SIM_PORT=10000
SCENARIO="${SUITE_ROOT}/test/robot/suites/payment_switch/scenarios/processor_sim.yaml"
SPEC="${SUITE_ROOT}/test/robot/suites/payment_switch/specs/switch.yaml"

MIXER_PID=""
RACK_PID=""

teardown() {
    [ -n "$RACK_PID" ] && kill "$RACK_PID" 2>/dev/null
    [ -n "$MIXER_PID" ] && kill "$MIXER_PID" 2>/dev/null
    wait 2>/dev/null
}
trap teardown EXIT

banner "Processor Simulator E2E"

# --- Build binaries ---
log_info "Building binaries..."
(cd "${SUITE_ROOT}" && make build-bin >/dev/null 2>&1) || fail "build-bin failed"

# --- Free ports ---
lsof -ti :$MIXER_API_PORT -ti :$SNAKE_PORT -ti :$SIM_PORT 2>/dev/null | xargs kill -9 2>/dev/null || true

# --- Workspace + configs ---
setup_workspace "processor_sim" "$BASE_DIR"
cp "${BASE_DIR}/mixer/fluxrig.toml" "${WORK_DIR}/mixer/fluxrig.toml"
cp "${BASE_DIR}/rack/fluxrig.toml"  "${WORK_DIR}/rack/fluxrig.toml"
mkdir -p "${WORK_DIR}/rack/specs"
cp "${SPEC}" "${WORK_DIR}/rack/specs/switch.yaml"

sed -i.bak "s/port = 9120/port = $MIXER_API_PORT/" "${WORK_DIR}/mixer/fluxrig.toml"
sed -i.bak "s/port = 4233/port = $SNAKE_PORT/g" "${WORK_DIR}/mixer/fluxrig.toml"
sed -i.bak "s#nats://127.0.0.1:4233#nats://127.0.0.1:$SNAKE_PORT#" "${WORK_DIR}/mixer/fluxrig.toml"
sed -i.bak "s#nats://127.0.0.1:4233#nats://127.0.0.1:$SNAKE_PORT#" "${WORK_DIR}/rack/fluxrig.toml"

# --- Start Mixer ---
section "Starting Mixer + Rack"
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "./data/cluster.key" >/dev/null 2>&1
"${ROOT_DIR}/bin/fluxrig-mixer" -c "fluxrig.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
wait_for_port $MIXER_API_PORT 15 || fail "Mixer failed to start"

# --- Start Rack ---
cd "${WORK_DIR}/rack"
mkdir -p logs && touch logs/rack.log
FLUXRIG_TRACE=1 "${ROOT_DIR}/bin/fluxrig" run -c "fluxrig.toml" > "rack.stdout" 2>&1 &
RACK_PID=$!
sleep 3

# --- Deploy the processor-sim scenario ---
section "Deploying processor_sim scenario"
export FLUXRIG_API_URL="http://127.0.0.1:${MIXER_API_PORT}"
status=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${FLUXRIG_API_URL}/api/v1/scenario/import?activate=true" \
  -H "Content-Type: application/x-yaml" --data-binary @"${SCENARIO}")
[ "$status" = "200" ] || fail "scenario import failed (HTTP $status)"
wait_for_port $SIM_PORT 20 || fail "sim-ingress failed to bind :$SIM_PORT"
sleep 2

# --- Drive the terminal: send ISO 0800 requests, expect authored 0810 replies ---
section "Terminal -> Processor Sim"
REPORT="${WORK_DIR}/procsim_report.json"
"${ROOT_DIR}/bin/iso8583-tool" -mode load \
  -target "127.0.0.1:${SIM_PORT}" -encoding ascii \
  -concurrency 2 -rate 10 -duration 3s \
  -report "${REPORT}" > "${WORK_DIR}/tool.stdout" 2>&1
tool_rc=$?

# --- Assertions ---
section "Verification"
FAILS=0

if [ $tool_rc -ne 0 ]; then log_error "load tool exited $tool_rc"; FAILS=$((FAILS+1)); fi

if [ -f "$REPORT" ]; then
    sent=$(python3 -c "import json;print(json.load(open('$REPORT')).get('req_sent',0))")
    recv=$(python3 -c "import json;print(json.load(open('$REPORT')).get('resp_recv',0))")
    rtt=$(python3 -c "import json;print(json.load(open('$REPORT')).get('rtt_success',0))")
    log_info "Terminal report: sent=$sent recv=$recv rtt_success=$rtt"
    [ "$recv" -gt 0 ] 2>/dev/null || { log_error "no responses received from the sim"; FAILS=$((FAILS+1)); }
    [ "$rtt" -gt 0 ] 2>/dev/null || { log_error "no RTT-correlated responses (header not mirrored)"; FAILS=$((FAILS+1)); }
else
    log_error "no report produced"; FAILS=$((FAILS+1))
fi

# The sim must have authored + encoded a response (codec logs the response MTI).
if grep -q "direction=encode.*mti=0810" "${WORK_DIR}/rack/rack.stdout" "${WORK_DIR}/rack/logs/rack.log" 2>/dev/null; then
    log_success "Processor sim authored an 0810 response (bento + codec encode confirmed)"
else
    log_error "no encoded 0810 response found in the rack log"; FAILS=$((FAILS+1))
fi

if [ "$FAILS" -eq 0 ]; then
    banner "PROCESSOR SIMULATOR E2E PASSED"
    exit 0
else
    banner "PROCESSOR SIMULATOR E2E FAILED ($FAILS checks)"
    exit 1
fi
