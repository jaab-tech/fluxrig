#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Simple E2E Test - Registration & Passport Verification, over TLS
# (was test/e2e/08_tls_simple)
#
# Isolated segment: a TLS-only Mixer/Rack can't enroll a plaintext Rack
# (or the other way around), so this can't share a lifecycle with anything
# else. Runs on its own ports (8102/4302 — see
# the port table in the internal e2e-consolidation design notes).
# Cleanup here is PID-tracked and port-scoped only, never
# `pkill -f "bin/fluxrig"` (see the same doc's "Constraint found during
# implementation" note).
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../../.." && pwd)"

source "${ROOT_DIR}/test/e2e/utils/e2e_utils.sh"

setup_workspace "tls_simple" "$BASE_DIR"

banner "Simple E2E Test (TLS)"

API_PORT=8102
SNAKE_PORT=4302
API_URL="https://localhost:${API_PORT}/api/v1"

MIXER_LOG="$WORK_DIR/mixer/mixer.stdout"
RACK_LOG="$WORK_DIR/rack/rack.stdout"

MIXER_PID=""
RACK_PID=""

cleanup() {
    echo ""
    log_info "Shutting down..."
    if [ -n "$MIXER_PID" ]; then
        kill -9 $MIXER_PID 2>/dev/null || true
        wait $MIXER_PID 2>/dev/null || true
    fi
    if [ -n "$RACK_PID" ]; then
        kill -9 $RACK_PID 2>/dev/null || true
        wait $RACK_PID 2>/dev/null || true
    fi
}
trap cleanup EXIT

# 0. This segment owns 8102/4302 exclusively; clearing them is scoped to
# those two ports, never a blanket process-name kill.
lsof -ti :${API_PORT} | xargs kill -9 2>/dev/null || true
lsof -ti :${SNAKE_PORT} | xargs kill -9 2>/dev/null || true

log_info "Generating Keys & Certs..."
"$(bin_dir)/fluxrig" keys gen-cluster -o "${WORK_DIR}/mixer/data/cluster.key" > /dev/null

mkdir -p "${WORK_DIR}/certs"
openssl genrsa -out "${WORK_DIR}/certs/ca.key" 2048 2>/dev/null
openssl req -new -x509 -days 365 -key "${WORK_DIR}/certs/ca.key" -subj "/C=UR/ST=MVD/L=MVD/O=JAAB/CN=fluxrig Root CA" -out "${WORK_DIR}/certs/ca.crt" 2>/dev/null
openssl genrsa -out "${WORK_DIR}/certs/server.key" 2048 2>/dev/null
openssl req -new -key "${WORK_DIR}/certs/server.key" -subj "/C=UR/ST=MVD/L=MVD/O=JAAB/CN=localhost" -out "${WORK_DIR}/certs/server.csr" 2>/dev/null
echo "subjectAltName=DNS:localhost,IP:127.0.0.1,DNS:flux" > "${WORK_DIR}/certs/extfile.cnf"
openssl x509 -req -days 365 -in "${WORK_DIR}/certs/server.csr" -CA "${WORK_DIR}/certs/ca.crt" -CAkey "${WORK_DIR}/certs/ca.key" -CAcreateserial -out "${WORK_DIR}/certs/server.crt" -extfile "${WORK_DIR}/certs/extfile.cnf" 2>/dev/null

CA_CERT="${WORK_DIR}/certs/ca.crt"
log_success "Keys and Certs generated."

log_info "Starting Mixer (TLS Enabled)..."
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
# Portable in-place edit: the original used `sed -i ''` (BSD/macOS syntax),
# which on GNU sed treats '' as the script and the real script as a
# filename — a no-op that silently leaves the relative ../certs paths in
# place instead of erroring loudly. `sed ... file > tmp && mv` works on both.
sed "s|\.\./certs|${WORK_DIR}/certs|g" "${WORK_DIR}/mixer/mixer.toml" > "${WORK_DIR}/mixer/mixer.toml.tmp" && mv "${WORK_DIR}/mixer/mixer.toml.tmp" "${WORK_DIR}/mixer/mixer.toml"

cd "${WORK_DIR}/mixer"
"$(bin_dir)/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"
log_info "Mixer PID: $MIXER_PID"

log_info "Waiting for Mixer Health (HTTPS)..."
MAX_RETRIES=30
for ((i=1;i<=MAX_RETRIES;i++)); do
    HTTP_CODE=$(curl -s --cacert "$CA_CERT" -o /dev/null -w "%{http_code}" "$API_URL/health")
    if [ "$HTTP_CODE" == "200" ]; then
        log_success "Mixer is UP."
        break
    fi
    sleep 1
    if [ $i -eq $MAX_RETRIES ]; then
        cat "$MIXER_LOG"
        fail "Mixer start timeout."
    fi
done

log_info "Starting Rack..."
cp "${BASE_DIR}/rack/rack.toml" "${WORK_DIR}/rack/rack.toml"
sed "s|\.\./certs|${WORK_DIR}/certs|g" "${WORK_DIR}/rack/rack.toml" > "${WORK_DIR}/rack/rack.toml.tmp" && mv "${WORK_DIR}/rack/rack.toml.tmp" "${WORK_DIR}/rack/rack.toml"

cd "${WORK_DIR}/rack"
"$(bin_dir)/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PID=$!
cd "${BASE_DIR}"
log_info "Rack PID: $RACK_PID"

log_info "Verifying Registration (Expecting 'rack-e2e-01')..."
FOUND=0
for ((i=1;i<=30;i++)); do
    curl -s --cacert "$CA_CERT" --max-time 2 "$API_URL/racks" > "$WORK_DIR/mixer/logs/api_racks.json"
    if grep -q "rack-e2e-01" "$WORK_DIR/mixer/logs/api_racks.json"; then
        log_success "Rack Registered and Found in API!"
        FOUND=1
        break
    fi
    sleep 1
done

if [ $FOUND -eq 0 ]; then
    echo "--- Rack Log ---"
    cat "$RACK_LOG"
    echo "--- Mixer Log (Tail) ---"
    tail -n 20 "$MIXER_LOG"
    fail "Timeout waiting for registration."
fi

log_info "Verifying Passport (state.flux)..."
STATE_FILE="$WORK_DIR/rack/data/state.flux"
if wait_for_file "$STATE_FILE" 15; then
    log_success "Passport found at $STATE_FILE"
    INSPECT_OUT=$("$(bin_dir)/fluxrig" keys inspect "$STATE_FILE")
    echo "$INSPECT_OUT"
    if echo "$INSPECT_OUT" | grep -q "MixerKey"; then
        log_success "MixerKey is populated."
    else
        fail "MixerKey is EMPTY!"
    fi
else
    echo "--- Rack Log (Tail) ---"
    tail -n 20 "$RACK_LOG"
    fail "Passport MISSING at $STATE_FILE"
fi

log_info "Verifying DB Content (Snapshot)..."
cp "$WORK_DIR/mixer/data/flux.duckdb" "$WORK_DIR/mixer/data/snapshot.duckdb"
[ -f "$WORK_DIR/mixer/data/flux.duckdb.wal" ] && cp "$WORK_DIR/mixer/data/flux.duckdb.wal" "$WORK_DIR/mixer/data/snapshot.duckdb.wal"

DB_OUT=$(duckdb -readonly -csv -noheader -c "SELECT name, machine_id FROM registry WHERE name='rack-e2e-01' AND type_id=4;" "$WORK_DIR/mixer/data/snapshot.duckdb")
rm -f "$WORK_DIR/mixer/data/snapshot.duckdb" "$WORK_DIR/mixer/data/snapshot.duckdb.wal"
if [[ "$DB_OUT" == *"rack-e2e-01"* ]]; then
    log_success "DB Verification Passed (Rack found in DB)"
else
    fail "DB Verification Failed (Rack not found in DB snapshot)"
fi

log_info "Letting agent run for heartbeats (5s)..."
sleep 5

banner "SUCCESS: All checks passed."
