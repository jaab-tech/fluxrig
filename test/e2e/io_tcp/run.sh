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
# Simple TCP Gear Regression Test
# ==============================================================================
# Objectives:
# 1. Validate 'io_tcp' I/O (Server/Client/Mode/Delimiters)
# 2. Validate Infrastructure Persistence (Port availability for Mixer/Snake)
# 3. Validate structured Logs and Traces
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "io_tcp" "$BASE_DIR"

# Infrastructure Ports
MIXER_API_PORT=9110
SNAKE_PORT=4223

banner "Simple TCP Gear Regression Test"

# Cleanup
log_info "Performing cleanup of ports: $MIXER_API_PORT, $SNAKE_PORT, 9001, 9002"
lsof -ti :$MIXER_API_PORT -ti :$SNAKE_PORT -ti :9001 -ti :9002 | xargs kill -9 2>/dev/null || true
sleep 1

# Initial Configs
cp "${BASE_DIR}/mixer/fluxrig.toml" "${WORK_DIR}/mixer/fluxrig.toml"
cp "${BASE_DIR}/rack/fluxrig.toml" "${WORK_DIR}/rack/fluxrig.toml"
cp "${BASE_DIR}/scenario.yaml" "${WORK_DIR}/mixer/scenario.yaml"

# 2. Compile
log_info "Compiling..."
cd "${ROOT_DIR}"
make build > /dev/null

# 3. Start Phase 1 (Server Mode)
log_info "--- Phase 1: Server Mode ---"
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "./data/cluster.key" > /dev/null 2>&1
"${ROOT_DIR}/bin/fluxrig-mixer" -c "fluxrig.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!

wait_for_port $MIXER_API_PORT 10 || fail "Mixer failed to start"

cd "${WORK_DIR}/rack"
unset FLUXRIG_DISABLE_TELEMETRY
FLUXRIG_TRACE=1 "${ROOT_DIR}/bin/fluxrig" run -c "fluxrig.toml" > "rack.stdout" 2>&1 &
AGENT_PID=$!
sleep 5

# Import Scenario
log_info "Importing Phase 1 Scenario..."
export FLUXRIG_API_URL="http://127.0.0.1:${MIXER_API_PORT}"
curl -s -X POST "${FLUXRIG_API_URL}/api/v1/scenario/import?activate=true" \
  -H "Content-Type: application/x-yaml" \
  --data-binary @"${WORK_DIR}/mixer/scenario.yaml" || log_warn "Import failed"

sleep 2
grep -q "Scenario Applied Successfully" "logs/rack.log" || fail "Scenario P1 failed to apply"

# Verification P1
log_info "Verifying I/O (:9001)..."
RESPONSE=$( (echo "TEST_MSG"; sleep 1) | nc -w 2 127.0.0.1 9001 )
log_info "Response: $RESPONSE"
[[ "$RESPONSE" == "TEST_MSG"* ]] || fail "I/O P1 Failed: '$RESPONSE'"
log_success "Phase 1 OK"

# 3.5 Runtime Visibility Check
log_info "--- Visibility: CLI Configuration ---"
"${ROOT_DIR}/bin/fluxrig" configuration --api-url "http://127.0.0.1:${MIXER_API_PORT}" || fail "CLI configuration failed"
log_success "CLI Visibility OK"

# 4. Graceful Shutdown & Restart
log_info "--- Transition: Graceful Shutdown ---"
# Stop Agent First
kill -TERM $AGENT_PID 2>/dev/null || true
wait $AGENT_PID 2>/dev/null || true
log_info "Agent Shutdown Cleanly"

# Stop Mixer
kill -TERM $MIXER_PID 2>/dev/null || true
wait $MIXER_PID 2>/dev/null || true
log_info "Mixer Shutdown Cleanly"

# Wait for sockets to settle (optional but safer for high-frequency CI)
sleep 10

# 5. Phase 2 (Client Mode)
log_info "--- Phase 2: Client Mode (Same Ports) ---"
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig-mixer" -c "fluxrig.toml" >> "mixer.stdout" 2>&1 &
MIXER_PID=$!
wait_for_port $MIXER_API_PORT 10 || fail "Mixer Phase 2 failed to restart"

log_info "Starting Mock Server (:9002)..."
python3 "${BASE_DIR}/mock_server.py" > "mock_server.stdout" 2>&1 &
MOCK_PID=$!

log_info "Importing Phase 2 Scenario..."
curl -s -X POST "${FLUXRIG_API_URL}/api/v1/scenario/import?activate=true" \
  -H "Content-Type: application/x-yaml" \
  --data-binary @"${BASE_DIR}/scenario_client.yaml" || fail "Scenario P2 import failed"

cd "${WORK_DIR}/rack"
unset FLUXRIG_DISABLE_TELEMETRY
FLUXRIG_TRACE=1 "${ROOT_DIR}/bin/fluxrig" run -c "fluxrig.toml" >> "rack.stdout" 2>&1 &
AGENT_PID=$!

log_info "Waiting for Mock Server verification..."
wait $MOCK_PID || fail "Phase 2 Verification Failed"
log_success "Phase 2 OK"

log_info "--- Visibility: CLI Configuration (Phase 2) ---"
"${ROOT_DIR}/bin/fluxrig" configuration --api-url "http://127.0.0.1:${MIXER_API_PORT}" || fail "CLI configuration failed"
log_success "CLI Visibility Phase 2 OK"

# 6. Deep Validation
log_info "Verifying Traces..."
# Verify traces in rack.log
if ! python3 "${ROOT_DIR}/test/e2e/utils/verify_trace.py" "${WORK_DIR}/rack/logs/rack.log"; then
  fail "Trace verification failed"
fi

# Ensure telemetry flushes before shutdown (OTel batch is 5s, Parquet flush is 2s)
log_info "Waiting for telemetry flush..."
sleep 10

# Final Cleanup
kill -TERM $AGENT_PID $MIXER_PID 2>/dev/null || true
wait $AGENT_PID $MIXER_PID 2>/dev/null || true
sleep 1

log_info "--- Registry: Full Contents ---"
if command -v duckdb >/dev/null 2>&1; then
  duckdb -c "SELECT entity_id, type_id, machine_id, name, status FROM registry ORDER BY type_id, entity_id" "${WORK_DIR}/mixer/data/fluxrig.duckdb"
else
  log_warn "duckdb CLI not found, skipping table dump. Use 'fluxrig configuration' for subset."
fi

# 7. Telemetry & Parquet Validation
log_info "--- Telemetry: Parquet Validation ---"
TELEMETRY_DIR="${WORK_DIR}/mixer/data/telemetry"

if [ -d "${TELEMETRY_DIR}" ]; then
  PARQUET_COUNT=$(find "${TELEMETRY_DIR}" -name "*.parquet" 2>/dev/null | wc -l | tr -d ' ')
  if [ "$PARQUET_COUNT" -gt 0 ]; then
    log_success "Found $PARQUET_COUNT Parquet file(s)"
    
    # Show Spans
    SPANS_DIR="${TELEMETRY_DIR}/spans"
    if [ -d "${SPANS_DIR}" ]; then
      log_info "--- Telemetry Spans ---"
      duckdb -c "SELECT * FROM read_parquet('${SPANS_DIR}/**/*.parquet')" 2>/dev/null || log_warn "No span data"
    else
      log_warn "Spans directory not found"
    fi
    
    # Show Logs
    LOGS_DIR="${TELEMETRY_DIR}/logs"
    if [ -d "${LOGS_DIR}" ]; then
      log_info "--- Telemetry Logs ---"
      duckdb -c "SELECT * FROM read_parquet('${LOGS_DIR}/**/*.parquet')" 2>/dev/null || log_warn "No log data"
    else
      log_warn "Logs directory not found"
    fi
    
    # Show Metrics
    METRICS_DIR="${TELEMETRY_DIR}/metrics"
    if [ -d "${METRICS_DIR}" ]; then
      log_info "--- Telemetry Metrics ---"
      duckdb -c "SELECT * FROM read_parquet('${METRICS_DIR}/**/*.parquet')" 2>/dev/null || log_warn "No metric data"
    else
      log_warn "Metrics directory not found"
    fi
    
    # Validation: Distributed Tracing (Context Propagation)
    # We expect the loopback traffic (gateway.out -> gateway.in) to create linked spans.
    # Span A (Ingress/Emit) -> Span B (Process/Handler). Span B should have parent_id = Span A.
    TRACING_CHECK=$(duckdb -noheader -csv -c "SELECT count(*) FROM read_parquet('${SPANS_DIR}/**/*.parquet') WHERE parent_span_id != '' AND parent_span_id IS NOT NULL" 2>/dev/null || echo 0)
    
    if [ "$TRACING_CHECK" -gt 0 ]; then
         log_success "Distributed Tracing Verified ($TRACING_CHECK linked spans found)."
         # Optional debug: show them
         # duckdb -c "SELECT trace_id, span_id, parent_id, name FROM read_parquet('${SPANS_DIR}/**/*.parquet') WHERE parent_id != ''"
    else
         # Fallback: Maybe we didn't generate enough traffic or export batching missed it?
         # But we waited 10s.
         log_warn "No linked spans found (parent_id is empty). Context Propagation might be failing."
         # We won't fail the whole test yet as users might run this without the new binary logic?
         # Actually, force strictness if we want to confirm the fix.
         fail "Distributed Tracing Verification Failed! No spans with parent_id found."
    fi

    log_success "Telemetry Pipeline Validated"
  else
    log_warn "No Parquet files found in ${TELEMETRY_DIR}. Telemetry may not have flushed yet."
  fi
else
  log_warn "Telemetry directory not found: ${TELEMETRY_DIR}"
fi

# 8. Log Parity Check (Physical vs Parquet)
log_info "--- Log Parity Check ---"
if command -v duckdb >/dev/null 2>&1; then
  python3 "${ROOT_DIR}/test/e2e/utils/compare_logs.py" "${WORK_DIR}" || fail "Log Parity Check Failed"
else
  log_warn "duckdb CLI not found, skipping log parity check."
fi

# Uniqueness check: Gear IDs should be different between Phase 1 and 2
# We check the Parquet logs as the source of truth (Telemetry verification)
log_info "--- Telemetry: Gear ID Uniqueness ---"
if command -v duckdb >/dev/null 2>&1; then
  # Extract distinct EIDs from logs where message contains "registered gear"
  # Note: attributes is a map/struct in DuckDB. We need to extract 'eid'.
  # Assuming attributes is a MAP(STRING, STRING) or Struct. In parquet it's properly typed.
  # We query 'telemetry_logs' table logic or read_parquet directly.
  # Attributes might be nested. `attributes['eid']` or `json_extract`.
  # For now, let's use the DuckDB power.
  # The parquet file structure: timestamp, severity, body, attributes (Map).
  
  # Wait for flush to ensure logs are there
  sleep 10
  
  RAW_IDS=$(duckdb -c "COPY (SELECT count(DISTINCT attributes->>'eid') FROM read_parquet('${WORK_DIR}/mixer/data/telemetry/logs/**/*.parquet') WHERE body LIKE '%registered gear%') TO stdout (FORMAT CSV, HEADER FALSE)")
  GEAR_IDS=$(echo "$RAW_IDS" | tr -d '[:space:]')
  
  if [ "$GEAR_IDS" -lt 2 ]; then
     # Fallback: Maybe 'msg' attribute?
     log_warn "Gear IDs found: $GEAR_IDS. Checking legacy..."
     RAW_IDS=$(duckdb -c "COPY (SELECT count(DISTINCT attributes->>'eid') FROM read_parquet('${WORK_DIR}/mixer/data/telemetry/logs/**/*.parquet') WHERE attributes->>'msg' LIKE '%registered gear%') TO stdout (FORMAT CSV, HEADER FALSE)")
     GEAR_IDS=$(echo "$RAW_IDS" | tr -d '[:space:]')
  fi
  if [ "$GEAR_IDS" -lt 2 ]; then
      fail "Gear ID collision or missing logs (Count: $GEAR_IDS)"
  fi
  log_success "Entity ID Uniqueness Validated ($GEAR_IDS unique gears)"
else
  log_warn "DuckDB not found, skipping Gear ID check"
fi

log_success "E2E Test COMPLETED"
exit 0
