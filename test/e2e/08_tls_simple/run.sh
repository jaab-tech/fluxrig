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
# Simple E2E Test - Registration & Passport Verification
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "tls_simple" "$BASE_DIR"

banner "Simple E2E Test (TLS)"

# Config
MIXER_CONFIG="$BASE_DIR/mixer/mixer.toml"
RACK_CONFIG="$BASE_DIR/rack/rack.toml"
API_URL="https://localhost:8090/api/v1"

MIXER_LOG="$WORK_DIR/mixer/mixer.stdout"
RACK_LOG="$WORK_DIR/rack/rack.stdout"

MIXER_PID=""
RACK_PID=""

# Override cleanup for this test
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
    pkill -P $$ 2>/dev/null || true
}
trap cleanup EXIT

# 1. Clean
log_info "Killing old processes..."
pkill -f "bin/fluxrig" || true
sleep 1
# ensure ports are free
lsof -ti :8090 | xargs kill -9 2>/dev/null || true
lsof -ti :4223 | xargs kill -9 2>/dev/null || true

# 2. Build
log_info "Building..."
cd "${ROOT_DIR}"
make build > "${WORK_DIR}/mixer/logs/build.log" 2>&1
if [ $? -ne 0 ]; then
    fail "Build failed. Check ${WORK_DIR}/mixer/logs/build.log"
fi

# 3. Keys & Certs
log_info "Generating Keys & Certs..."
"${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "${WORK_DIR}/mixer/data/cluster.key" > /dev/null

mkdir -p "${WORK_DIR}/certs"
openssl genrsa -out "${WORK_DIR}/certs/ca.key" 2048 2>/dev/null
openssl req -new -x509 -days 365 -key "${WORK_DIR}/certs/ca.key" -subj "/C=UR/ST=MVD/L=MVD/O=JAAB/CN=FluxRig Root CA" -out "${WORK_DIR}/certs/ca.crt" 2>/dev/null
openssl genrsa -out "${WORK_DIR}/certs/server.key" 2048 2>/dev/null
openssl req -new -key "${WORK_DIR}/certs/server.key" -subj "/C=UR/ST=MVD/L=MVD/O=JAAB/CN=localhost" -out "${WORK_DIR}/certs/server.csr" 2>/dev/null
echo "subjectAltName=DNS:localhost,IP:127.0.0.1" > "${WORK_DIR}/certs/extfile.cnf"
openssl x509 -req -days 365 -in "${WORK_DIR}/certs/server.csr" -CA "${WORK_DIR}/certs/ca.crt" -CAkey "${WORK_DIR}/certs/ca.key" -CAcreateserial -out "${WORK_DIR}/certs/server.crt" -extfile "${WORK_DIR}/certs/extfile.cnf" 2>/dev/null

CA_CERT="${WORK_DIR}/certs/ca.crt"

log_success "Keys and Certs generated."

# 4. Start Mixer
log_info "Starting Mixer (TLS Enabled)..."
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
# Use absolute paths for Certs
sed -i '' "s|\.\./certs|${WORK_DIR}/certs|g" "${WORK_DIR}/mixer/mixer.toml"

cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
log_info "Mixer PID: $MIXER_PID"

# Wait for Mixer
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

# 5. Start Rack
log_info "Starting Rack..."
cp "${BASE_DIR}/rack/rack.toml" "${WORK_DIR}/rack/rack.toml"
sed -i '' "s|\.\./certs|${WORK_DIR}/certs|g" "${WORK_DIR}/rack/rack.toml"

cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PID=$!
log_info "Rack PID: $RACK_PID"

# 6. Verify Registration (Loop)
log_info "Verifying Registration (Expecting 'rack-e2e-01')..."
FOUND=0
for ((i=1;i<=30;i++)); do
    curl -s --cacert "$CA_CERT" --max-time 2 "$API_URL/racks" > "$WORK_DIR/mixer/logs/api_racks.json"
    if grep -q "rack-e2e-01" "$WORK_DIR/mixer/logs/api_racks.json"; then
        log_success "Rack Registered and Found in API!"
        FOUND=1
        break
     else
          if (( i % 5 == 0 )); then
              log_info "(API returned: $(cat $WORK_DIR/mixer/logs/api_racks.json))"
          fi
     fi
    sleep 1
    echo -n "."
done
echo ""

if [ $FOUND -eq 0 ]; then
    echo "--- Rack Log ---"
    cat "$RACK_LOG"
    echo "--- Mixer Log (Tail) ---"
    tail -n 20 "$MIXER_LOG"
    fail "Timeout waiting for registration."
fi

# 6.5 Verify State File (Passport)
log_info "Verifying Passport (state.flux)..."
STATE_FILE="$WORK_DIR/rack/data/state.flux"
if [ -f "$STATE_FILE" ]; then
    log_success "Passport found at $STATE_FILE"
    log_info "Inspecting Passport..."
    INSPECT_OUT=$( "${ROOT_DIR}/bin/fluxrig" keys inspect "$STATE_FILE" )
    echo "$INSPECT_OUT"
    
    # Validate MixerID is present (binary, hex format)
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

# 7. List Racks (CLI Check)
# log_info "Verifying CLI..."
# "${ROOT_DIR}/bin/fluxrig" admin --api-url "https://localhost:8090" racks list > "$WORK_DIR/mixer/logs/cli_list.txt" 2>&1
# if grep -q "rack-e2e-01" "$WORK_DIR/mixer/logs/cli_list.txt"; then
#     log_success "CLI lists the rack."
# else
#     echo "SKIPPED: CLI does not checking CA certs yet."
#     # cat "$WORK_DIR/mixer/logs/cli_list.txt"
#     # fail "CLI failed to list rack."
# fi

# 8. Verify DB Tables and Content
log_info "Verifying DB Content (Snapshot)..."
cp "$WORK_DIR/mixer/data/fluxrig_test.duckdb" "$WORK_DIR/mixer/data/snapshot.duckdb"
[ -f "$WORK_DIR/mixer/data/fluxrig_test.duckdb.wal" ] && cp "$WORK_DIR/mixer/data/fluxrig_test.duckdb.wal" "$WORK_DIR/mixer/data/snapshot.duckdb.wal"

log_info "Checking 'registry' table (Racks)..."
DB_OUT=$(duckdb -readonly -csv -noheader -c "SELECT name, machine_id FROM registry WHERE name='rack-e2e-01' AND type_id=4;" "$WORK_DIR/mixer/data/snapshot.duckdb")
log_info "DB Row: $DB_OUT"

if [[ "$DB_OUT" == *"rack-e2e-01"* ]]; then
    log_success "DB Verification Passed (Rack found in DB)"
else
    fail "DB Verification Failed (Rack not found in DB snapshot)"
fi

rm -f "$WORK_DIR/mixer/data/snapshot.duckdb" "$WORK_DIR/mixer/data/snapshot.duckdb.wal"

# 9. Wait for Heartbeats
log_info "Letting agent run for heartbeats (5s)..."
sleep 5

banner "SUCCESS: All checks passed."
