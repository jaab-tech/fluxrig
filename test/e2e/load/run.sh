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

set -e

# ==============================================================================
# Bento Load Generator - Smoke Test
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "e2e_load" "$BASE_DIR"

# Ports (avoid collision with default 9110/4223)
MIXER_API_PORT=9115
SNAKE_PORT=4225

banner "Bento Load Generator Smoke Test"

# Cleanup
log_info "Cleaning up ports: $MIXER_API_PORT, $SNAKE_PORT"
lsof -ti :$MIXER_API_PORT -ti :$SNAKE_PORT | xargs kill -9 2>/dev/null || true
rm -f /tmp/flux_e2e_load_out.txt

# 1. Configs
log_info "Deploying Configs..."

# Copy Mixer Config and Scenario
mkdir -p "${WORK_DIR}/mixer" "${WORK_DIR}/rack"
cp "${BASE_DIR}/mixer/fluxrig.toml" "${WORK_DIR}/mixer/fluxrig.toml"
cp "${BASE_DIR}/rack/fluxrig.toml" "${WORK_DIR}/rack/fluxrig.toml"

# 2. Compile
log_info "Compiling..."
cd "${ROOT_DIR}"
make build > /dev/null

# 3. Start Infrastructure
log_info "--- Phase 1: Start Infrastructure ---"

# Start Mixer
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "./data/cluster.key" > /dev/null 2>&1
"${ROOT_DIR}/bin/fluxrig-mixer" -c "fluxrig.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
wait_for_port $MIXER_API_PORT 15 || fail "Mixer start failed"

# Start Rack
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" run -c "fluxrig.toml" > "rack.stdout" 2>&1 &
AGENT_PID=$!
wait_for_rack "http://127.0.0.1:${MIXER_API_PORT}/api/v1" "load-node-01" 15

# 4. Import Scenario
log_info "--- Phase 2: Import Scenario ---"
export FLUXRIG_API_URL="http://127.0.0.1:${MIXER_API_PORT}"

# Wait a bit for Rack to settle
sleep 2

RESPONSE=$(curl -s -X POST "${FLUXRIG_API_URL}/api/v1/scenario/import?activate=true" \
  -H "Content-Type: application/x-yaml" \
  --data-binary @"${BASE_DIR}/scenario.yaml")

log_info "Scenario Import Response: $RESPONSE"
if [[ "$RESPONSE" != *"imported_and_activated"* ]]; then
    fail "Scenario import failed"
fi

# 5. Validation
log_info "--- Phase 3: Validation ---"
log_info "Waiting for messages to flow (interval 1s, count 5)..."

# Wait enough time (5 count * 1s interval = 5s + buffer)
for i in {1..15}; do
    if [ -f "/tmp/flux_e2e_load_out.txt" ]; then
        LINES=$(wc -l < /tmp/flux_e2e_load_out.txt | tr -d ' ')
        log_info "Output lines: $LINES"
        if [ "$LINES" -ge 5 ]; then
            log_success "Received 5+ messages"
            break
        fi
    fi
    sleep 1
done

if [ ! -f "/tmp/flux_e2e_load_out.txt" ]; then
   fail "Output file not found"
fi

LINES=$(wc -l < /tmp/flux_e2e_load_out.txt | tr -d ' ')
if [ "$LINES" -lt 5 ]; then
    fail "Insufficient messages: $LINES (Expected 5)"
fi

# Cat file content for verification
log_info "Output Content:"
cat /tmp/flux_e2e_load_out.txt

# 5.5 Telemetry & Parquet Validation
log_info "--- Phase 4: Telemetry Validation ---"
# Ensure flush
sleep 5

TELEMETRY_DIR="${WORK_DIR}/mixer/data/telemetry"
if [ -d "${TELEMETRY_DIR}" ]; then
  PARQUET_COUNT=$(find "${TELEMETRY_DIR}" -name "*.parquet" 2>/dev/null | wc -l | tr -d ' ')
  if [ "$PARQUET_COUNT" -gt 0 ]; then
    log_success "Found $PARQUET_COUNT Parquet file(s)"
    
    # Optional: Inspect with DuckDB if available
    if command -v duckdb >/dev/null 2>&1; then
        LOGS_DIR="${TELEMETRY_DIR}/logs"
        if [ -d "${LOGS_DIR}" ]; then
          log_info "--- Telemetry Logs Preview ---"
          duckdb -c "SELECT timestamp, severity, body FROM read_parquet('${LOGS_DIR}/**/*.parquet') LIMIT 5" 2>/dev/null || log_warn "Failed to query logs"
        fi
        
        # Log Parity Check
        log_info "--- Log Parity Check ---"
        python3 "${ROOT_DIR}/test/e2e/utils/compare_logs.py" "${WORK_DIR}" || fail "Log Parity Check Failed"
    else
        log_warn "duckdb CLI not found, skipping deep inspection."
    fi
  else
    fail "No Parquet files found. Telemetry failed."
  fi
else
  fail "Telemetry directory not found: ${TELEMETRY_DIR}"
fi

# 6. Cleanup
log_info "--- Shutdown ---"
kill -TERM $AGENT_PID $MIXER_PID 2>/dev/null || true
wait $AGENT_PID $MIXER_PID 2>/dev/null || true

log_success "Load Test Smoke Pass ✅"
exit 0
