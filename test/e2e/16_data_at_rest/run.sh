#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Data at Rest E2E Test
#
# A message that carries a card number crosses from one Rack to another, over the
# Mixer's bus. With the default settings and the default log level, no file the
# Mixer or either Rack wrote may hold the number in clear.
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "data_at_rest" "$BASE_DIR"

banner "Data at Rest E2E Test"

API_URL="http://localhost:8090/api/v1"
PAN=4111111111111111
MESSAGES=20
STREAM_DIR="$WORK_DIR/mixer/data/snake/jetstream"

ENROLL_TIMEOUT=30
STREAM_TIMEOUT=30

MIXER_PID=""
A_PID=""
B_PID=""

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
    stop_pid "$A_PID"
    stop_pid "$B_PID"
}
trap cleanup EXIT

# 0. Prep
lsof -ti :8090 | xargs kill -9 2>/dev/null || true

# 1. Mixer, two Racks, one scenario. Nothing is configured: encryption at rest and
# the log level are the defaults.
"$(bin_dir)/fluxrig" keys gen-cluster -o "$WORK_DIR/mixer/data/cluster.key" > /dev/null
mkdir -p "$WORK_DIR/rack-a" "$WORK_DIR/rack-b"
cp "${BASE_DIR}/mixer/mixer.toml" "$WORK_DIR/mixer/mixer.toml"

cd "$WORK_DIR/mixer"
"$(bin_dir)/fluxrig-mixer" -c mixer.toml >> mixer.stdout 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"
wait_for_port 8090 30 || fail "Mixer API did not come up."

for r in a b; do
    cp "${BASE_DIR}/rack-$r/rack.toml" "$WORK_DIR/rack-$r/rack.toml"
    cd "$WORK_DIR/rack-$r"
    "$(bin_dir)/fluxrig" rack -c rack.toml >> rack.stdout 2>&1 &
    if [ "$r" == "a" ]; then A_PID=$!; else B_PID=$!; fi
    cd "${BASE_DIR}"
done

for r in a b; do
    deadline=$((SECONDS + ENROLL_TIMEOUT))
    until curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-$r"; do
        (( SECONDS < deadline )) || fail "rack-$r did not become active."
        sleep 0.5
    done
done
log_success "Both Racks are active."

HTTP_CODE=$(curl -s -o "$WORK_DIR/import.out" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
    -H "Content-Type: application/x-yaml" --data-binary @"${BASE_DIR}/scenario.yaml")
[ "$HTTP_CODE" == "200" ] || { cat "$WORK_DIR/import.out"; fail "Scenario import failed (HTTP $HTTP_CODE)."; }
wait_for_port 9621 30 || fail "gw-a is not listening."
wait_for_port 9622 30 || fail "gw-b is not listening."

# 2. Messages with a card number, into Rack A, meant for Rack B
log_info "Sending $MESSAGES messages carrying a card number..."
for i in $(seq 1 $MESSAGES); do
    exec 3<>/dev/tcp/127.0.0.1/9621
    printf '0100%s16%s000000010000%06d\n' "7234054128C28805" "$PAN" "$i" >&3
    exec 3>&- 3<&-
done

# 3. The messages must have reached the Mixer's stream, or the checks below prove
# nothing. Wait for it to hold something, then look at every file.
stream_bytes() {
    find "$STREAM_DIR" -path '*flux-msg*' -name '*.blk' -printf '%s\n' 2>/dev/null | awk '{s+=$1} END{print s+0}'
}
deadline=$((SECONDS + STREAM_TIMEOUT))
until [ "$(stream_bytes)" -gt $((MESSAGES * 100)) ]; do
    (( SECONDS < deadline )) || fail "The Mixer's message stream never received the traffic."
    sleep 0.5
done
sleep 2
log_success "The traffic reached the Mixer's message stream ($(stream_bytes) bytes)."

# 4. Nothing readable, anywhere
if grep -q "Snake store is encrypted at rest" "$WORK_DIR/mixer/logs/mixer.log"; then
    log_success "The Mixer reports that its store is encrypted."
else
    fail "The Mixer did not report an encrypted store."
fi

if [ -f "$WORK_DIR/mixer/data/snake/.store-encrypted" ]; then
    log_success "The store is marked as encrypted."
else
    fail "No encryption marker in the store directory."
fi

LEAKS=$(grep -rlaE "$PAN" "$WORK_DIR/mixer" "$WORK_DIR/rack-a" "$WORK_DIR/rack-b" 2>/dev/null | sed "s#$WORK_DIR/##")
if [ -z "$LEAKS" ]; then
    log_success "The card number is in no file the Mixer or the Racks wrote."
else
    log_error "The card number is readable in:"
    echo "$LEAKS"
    fail "A card number rests in clear."
fi

banner "Data at Rest Verified"
