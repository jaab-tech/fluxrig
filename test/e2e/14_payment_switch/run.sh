#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
#
# Full payment-switch validation: the Conductor routes 0200 authorizations by
# the PAN's BIN to scheme uplinks, correlates the reply, and returns it. First
# real end-to-end use of the Conductor gear. Covers all four outcomes:
#   BIN 4xxx -> scheme A (approve, DE39=00)
#   BIN 5xxx -> scheme B (decline, DE39=05)
#   BIN 6xxx -> scheme C (sink -> Conductor timeout -> decline DE39=91)
#   BIN 9xxx -> no route -> decline DE39=05
set -uo pipefail

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
SUITE_ROOT="$(cd "${BASE_DIR}/../../.." && pwd)"
source "${SUITE_ROOT}/test/e2e/utils/e2e_utils.sh"
ensure_root

MIXER_API_PORT=9140
SNAKE_PORT=4253
SWITCH_PORT=8583
SCENARIO="${SUITE_ROOT}/test/robot/suites/payment_switch/scenarios/switch.yaml"
SPEC="${SUITE_ROOT}/test/robot/suites/payment_switch/specs/auth.yaml"
TOOL="${ROOT_DIR}/bin/iso8583-tool"

MIXER_PID=""; RACK_PID=""; SCHEME_PIDS=()
teardown() {
    for p in "${SCHEME_PIDS[@]:-}"; do kill "$p" 2>/dev/null; done
    [ -n "$RACK_PID" ] && kill "$RACK_PID" 2>/dev/null
    [ -n "$MIXER_PID" ] && kill "$MIXER_PID" 2>/dev/null
    wait 2>/dev/null
}
trap teardown EXIT

banner "Payment Switch E2E (Conductor BIN routing)"

log_info "Building binaries..."
(cd "${SUITE_ROOT}" && make build-bin >/dev/null 2>&1) || fail "build-bin failed"

lsof -ti :$MIXER_API_PORT -ti :$SNAKE_PORT -ti :$SWITCH_PORT -ti :10001 -ti :10002 -ti :10003 2>/dev/null | xargs kill -9 2>/dev/null || true

setup_workspace "payment_switch" "$BASE_DIR"
cp "${BASE_DIR}/mixer/fluxrig.toml" "${WORK_DIR}/mixer/fluxrig.toml"
cp "${BASE_DIR}/rack/fluxrig.toml"  "${WORK_DIR}/rack/fluxrig.toml"
mkdir -p "${WORK_DIR}/rack/specs"
cp "${SPEC}" "${WORK_DIR}/rack/specs/auth.yaml"
sed -i.bak "s/port = 9120/port = $MIXER_API_PORT/" "${WORK_DIR}/mixer/fluxrig.toml"
sed -i.bak "s/port = 4233/port = $SNAKE_PORT/g" "${WORK_DIR}/mixer/fluxrig.toml"
sed -i.bak "s#nats://127.0.0.1:4233#nats://127.0.0.1:$SNAKE_PORT#" "${WORK_DIR}/mixer/fluxrig.toml"
sed -i.bak "s#nats://127.0.0.1:4233#nats://127.0.0.1:$SNAKE_PORT#" "${WORK_DIR}/rack/fluxrig.toml"

# --- Scheme hosts (the upstream card schemes) ---
section "Starting scheme hosts"
"${TOOL}" -mode scheme -scheme-port 10001 -scheme-spec "$SPEC" -scheme-de39 00 > "${WORK_DIR}/scheme_a.log" 2>&1 &
SCHEME_PIDS+=($!)
"${TOOL}" -mode scheme -scheme-port 10002 -scheme-spec "$SPEC" -scheme-de39 05 > "${WORK_DIR}/scheme_b.log" 2>&1 &
SCHEME_PIDS+=($!)
"${TOOL}" -mode scheme -scheme-port 10003 -scheme-spec "$SPEC" -scheme-sink > "${WORK_DIR}/scheme_c.log" 2>&1 &
SCHEME_PIDS+=($!)
wait_for_port 10001 10 || fail "scheme A failed"
wait_for_port 10002 10 || fail "scheme B failed"
wait_for_port 10003 10 || fail "scheme C failed"

# --- Mixer + switch rack ---
section "Starting Mixer + switch Rack"
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "./data/cluster.key" >/dev/null 2>&1
"${ROOT_DIR}/bin/fluxrig-mixer" -c "fluxrig.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
wait_for_port $MIXER_API_PORT 15 || fail "Mixer failed to start"

cd "${WORK_DIR}/rack"
mkdir -p logs && touch logs/rack.log
FLUXRIG_TRACE=1 "${ROOT_DIR}/bin/fluxrig" run -c "fluxrig.toml" > "rack.stdout" 2>&1 &
RACK_PID=$!
sleep 3

section "Deploying switch scenario"
export FLUXRIG_API_URL="http://127.0.0.1:${MIXER_API_PORT}"
status=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${FLUXRIG_API_URL}/api/v1/scenario/import?activate=true" \
  -H "Content-Type: application/x-yaml" --data-binary @"${SCENARIO}")
[ "$status" = "200" ] || fail "scenario import failed (HTTP $status)"
wait_for_port $SWITCH_PORT 20 || fail "switch ingress failed to bind :$SWITCH_PORT"
sleep 3   # let the uplink clients dial the schemes

# --- Single-shot edge cases (one txn each) ---
section "Edge-case routing outcomes"
FAILS=0
check() {
    local label="$1"; shift
    if "${TOOL}" -mode auth -auth-target "127.0.0.1:${SWITCH_PORT}" -auth-spec "$SPEC" "$@"; then
        log_success "$label"
    else
        log_error "$label"; FAILS=$((FAILS+1))
    fi
}
check "BIN 9xxx -> no_route -> decline" -auth-pan 9111111111111111 -auth-stan 000004 -auth-expect-mti 0210 -auth-expect-de39 05

# --- Concurrent multi-source load ---
# One terminal per scheme, each at its own TPS, PLUS one multi-scheme terminal
# fanning across both. Every txn carries a globally-unique STAN (distinct
# -auth-stan-base per process); each verifies the reply's STAN echoes its own
# request (no cross-wiring) and DE39 matches the scheme it targeted. Poisson
# arrivals + randomized PANs/amounts make the streams interleave unpredictably,
# which is the real correlation stress.
section "Concurrent multi-source load (per-scheme + multi-scheme terminals)"
LOAD_FAILS=0

# Terminal A: BIN 4 -> scheme A (approve), high TPS.
"${TOOL}" -mode auth -auth-target "127.0.0.1:${SWITCH_PORT}" -auth-spec "$SPEC" \
    -auth-pan 4 -auth-expect-mti 0210 -auth-expect-de39 00 \
    -auth-count 40 -auth-rate 25 -auth-stan-base 100000 \
    > "${WORK_DIR}/term_a.log" 2>&1 &
TA=$!
# Terminal B: BIN 5 -> scheme B (decline), lower TPS.
"${TOOL}" -mode auth -auth-target "127.0.0.1:${SWITCH_PORT}" -auth-spec "$SPEC" \
    -auth-pan 5 -auth-expect-mti 0210 -auth-expect-de39 05 \
    -auth-count 16 -auth-rate 8 -auth-stan-base 300000 \
    > "${WORK_DIR}/term_b.log" 2>&1 &
TB=$!
# Terminal M: multi-scheme, mixes BIN 4 (approve/00) and BIN 5 (decline/05).
"${TOOL}" -mode auth -auth-target "127.0.0.1:${SWITCH_PORT}" -auth-spec "$SPEC" \
    -auth-mix "4:00,5:05" -auth-expect-mti 0210 \
    -auth-count 30 -auth-rate 15 -auth-stan-base 500000 \
    > "${WORK_DIR}/term_m.log" 2>&1 &
TM=$!

wait_term() {
    local pid="$1" label="$2" logf="$3"
    if wait "$pid"; then
        log_success "$label -> $(tail -n1 "$logf")"
    else
        log_error "$label -> $(tail -n1 "$logf")"
        LOAD_FAILS=$((LOAD_FAILS+1))
    fi
}
wait_term "$TA" "terminal A (BIN4 @25tps)"       "${WORK_DIR}/term_a.log"
wait_term "$TB" "terminal B (BIN5 @8tps)"        "${WORK_DIR}/term_b.log"
wait_term "$TM" "terminal M (mix 4/5 @15tps)"    "${WORK_DIR}/term_m.log"
FAILS=$((FAILS + LOAD_FAILS))

# BIN 6xxx -> the scheme is a sink that never replies, so the Conductor's ticket
# expires and the timeout error is returned as a decline (DE39=91). Fixed by B5:
# the JetStream dedup key is now subject-scoped, so the timer-goroutine
# re-emission of the parked request no longer collides with the outbound copy
# that went to the scheme.
section "BIN 6xxx -> timeout -> decline"
check "BIN 6xxx -> timeout -> decline" \
    -auth-pan 6111111111111111 -auth-stan 000003 -auth-expect-mti 0210 -auth-expect-de39 91 -auth-timeout 8s

if [ "$FAILS" -eq 0 ]; then
    banner "PAYMENT SWITCH E2E PASSED"
    exit 0
else
    banner "PAYMENT SWITCH E2E FAILED ($FAILS failures)"
    exit 1
fi
