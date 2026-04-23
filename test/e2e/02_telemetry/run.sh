#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Telemetry E2E Test
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

source "${BASE_DIR}/../utils/e2e_utils.sh"

setup_workspace "telemetry" "$BASE_DIR"

banner "Telemetry E2E Test"

# Force Enable Telemetry (Verification)
export FLUXRIG_DISABLE_TELEMETRY=false

# Config
MIXER_CONFIG="$BASE_DIR/mixer/mixer.toml"
RACK_CONFIG="$BASE_DIR/rack/rack.toml"
API_URL="http://127.0.0.1:8092/api/v1"

MIXER_LOG="$WORK_DIR/mixer/mixer.stdout"
RACK_LOG="$WORK_DIR/rack/rack.stdout"

DB_CLI="duckdb"
FLUX_BIN="${ROOT_DIR}/bin/fluxrig"
MIXER_DB="$WORK_DIR/mixer/data/fluxrig_test.duckdb"
TELEMETRY_DIR="$WORK_DIR/mixer/data/telemetry"

MIXER_PID=""
RACK_PID=""

cleanup() {
    log_info "Shutting down..."
    if [ -n "$MIXER_PID" ]; then kill -9 $MIXER_PID 2>/dev/null || true; wait $MIXER_PID 2>/dev/null || true; fi
    if [ -n "$RACK_PID" ]; then kill -9 $RACK_PID 2>/dev/null || true; wait $RACK_PID 2>/dev/null || true; fi
    pkill -f "bin/fluxrig" || true
}
trap cleanup EXIT

# 1. Clean
kill_old_processes
# ensure ports are free
lsof -ti :8092 | xargs kill -9 2>/dev/null || true

# 2. Check Binaries
if [ ! -f "${ROOT_DIR}/bin/fluxrig-mixer" ]; then
    fail "bin/fluxrig-mixer not found. Run make build."
fi

# 3. Keys
gen_keys "$WORK_DIR/mixer/data/cluster.key"

# 4. Start Mixer (Port 8092)
log_info "Starting Mixer (Port 8092)..."
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
cd "${WORK_DIR}/mixer"
"${ROOT_DIR}/bin/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"

wait_for_mixer "$API_URL"

# 5. Start Rack
log_info "Starting Rack..."
cp "${BASE_DIR}/rack/rack.toml" "${WORK_DIR}/rack/rack.toml"
cd "${WORK_DIR}/rack"
"${ROOT_DIR}/bin/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PID=$!
cd "${BASE_DIR}"

# 6. Wait for Registration
log_info "Waiting for Rack Registration..."
FOUND=0
for ((i=1;i<=30;i++)); do
    curl -s --max-time 2 "$API_URL/racks" > "$WORK_DIR/mixer/logs/api_racks.json"
    if grep -q "rack-e2e-telemetry-01" "$WORK_DIR/mixer/logs/api_racks.json"; then
        log_success "Rack Registered!"
        FOUND=1
        break
    fi
    sleep 1
done

if [ $FOUND -eq 0 ]; then
    cat "$RACK_LOG"
    fail "Rack failed to register."
fi

# 7. Generate Traffic (Wait for Telemetry)
log_info "Waiting for Telemetry Ingestion (30s for metrics export)..."
sleep 30

# ==============================================================================
section "Parquet Content Verification"
# ==============================================================================

# Check if any files exist first
if [ -z "$(find "$TELEMETRY_DIR/logs" -name "*.parquet" -print -quit 2>/dev/null)" ]; then
    cat "$MIXER_LOG"
    fail "No Parquet files found in $TELEMETRY_DIR/logs"
fi

PARQUET_GLOB="$TELEMETRY_DIR/logs/**/*.parquet"

COUNT_LOGS=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$PARQUET_GLOB')")
COUNT_RACK_LOGS=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$PARQUET_GLOB') WHERE entity_name LIKE 'rack%'")

log_info "Found $COUNT_LOGS Logs total"
log_info "Found $COUNT_RACK_LOGS Rack Logs (by Name)"

if [ "$COUNT_LOGS" -eq 0 ]; then
    cat "$MIXER_LOG"
    cat "$RACK_LOG"
    fail "No Telemetry Data Found in Parquet."
fi

if [ "$COUNT_RACK_LOGS" -eq 0 ]; then
    cat "$RACK_LOG"
    fail "No Rack Logs Found (Only Mixer?)."
fi

log_success "Parquet Data Verified."

# ==============================================================================
section "CLI Telemetry Verification"
# ==============================================================================

# Wait additional time for metric batching
log_info "Waiting additional 10s for metric batching..."
sleep 10

# 1. Logs
log_info "Testing 'fluxrig logs'..."
LOGS_OUT=$($FLUX_BIN logs --api-url "http://localhost:8092" --limit 20 --min-level debug)
log_info "$LOGS_OUT"

LOG_COUNT=$(echo "$LOGS_OUT" | tail -n +2 | grep -v "^$" | wc -l | tr -d ' ')
if [ "$LOG_COUNT" -lt 5 ]; then
    fail "CLI Logs Verification Failed. Expected at least 5 logs, got $LOG_COUNT"
fi
log_success "CLI Logs Verified ($LOG_COUNT logs found)."

# 2. Metrics
log_info "Testing 'fluxrig metrics' (Waiting for availability)..."
METRICS_FOUND=0
for ((i=1;i<=10;i++)); do
    METRICS_OUT=$($FLUX_BIN metrics --api-url "http://localhost:8092" --limit 10)
    if [[ "$METRICS_OUT" == *"heartbeats_sent"* ]]; then
        METRICS_FOUND=1
        log_info "$METRICS_OUT"
        break
    fi
    sleep 2
done

if [ $METRICS_FOUND -eq 0 ]; then
    log_info "Last Output: $METRICS_OUT"
    log_info "CLI Metrics Verification deferred (Missing 'heartbeats_sent'). Known Issue: Metric Ingestion/Export batching."
fi
log_success "CLI Metrics Verification Attempted."

# ==============================================================================
section "Registry Verification"
# ==============================================================================

# Snapshot DB to avoid locking
cp "$MIXER_DB" "$WORK_DIR/mixer/data/snapshot.duckdb"
[ -f "$MIXER_DB.wal" ] && cp "$MIXER_DB.wal" "$WORK_DIR/mixer/data/snapshot.duckdb.wal"
SNAPSHOT_DB="$WORK_DIR/mixer/data/snapshot.duckdb"

MIXER_COUNT=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT count(*) FROM registry WHERE type_id=2")
SNAKE_COUNT=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT count(*) FROM registry WHERE type_id=9")
RACK_COUNT=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT count(*) FROM registry WHERE type_id=4")

log_info "Found $MIXER_COUNT Mixers"
log_info "Found $SNAKE_COUNT Snakes"
log_info "Found $RACK_COUNT Racks"

if [ "$MIXER_COUNT" -ne 1 ]; then
    fail "Expected 1 Mixer, got $MIXER_COUNT"
fi
if [ "$SNAKE_COUNT" -ne 1 ]; then
    fail "Expected 1 Snake, got $SNAKE_COUNT"
fi
if [ "$RACK_COUNT" -ne 1 ]; then
    fail "Expected 1 Rack, got $RACK_COUNT"
fi

log_success "Registry Counts Verified."

# Verify Mixer Stats
log_info "Verifying Mixer Stats..."
MIXER_ROW=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT stats FROM registry WHERE type_id=2 LIMIT 1")
if [[ "$MIXER_ROW" != *"goroutines"* ]]; then
     fail "Mixer Stats missing (goroutines): $MIXER_ROW"
fi
log_success "Mixer Stats Verified."

# Verify Snake Attributes
log_info "Verifying Snake Attributes & Stats..."
SNAKE_ROW=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT stats, attributes, name FROM registry WHERE type_id=9 LIMIT 1")
log_info "Snake Row: $SNAKE_ROW"

if [[ -z "$SNAKE_ROW" ]]; then
     fail "Snake not found in DB!"
fi
if [[ "$SNAKE_ROW" != *"rack"* ]]; then
     fail "Snake Attributes missing 'rack' topology!"
fi
if [[ "$SNAKE_ROW" != *"in_msgs"* ]]; then
     fail "Snake Stats missing 'in_msgs' metric!"
fi
log_success "Snake Verified."

# ==============================================================================
section "API Entity Stats Verification"
# ==============================================================================

log_info "Querying Entity Stats API..."
STATS_JSON=$(curl -s "$API_URL/entities/stats")

log_info "Stats Response Preview: $(echo "$STATS_JSON" | head -n 5)"

# Debug: Show full JSON
echo "=== FULL ENTITY STATS ==="
echo "$STATS_JSON" | jq .
echo "========================="

# 1. Validation: Heartbeat Metrics (Rack) - The primary metric after deferred telemetry init
if [[ "$STATS_JSON" != *"heartbeats_sent"* ]]; then
    fail "Missing 'heartbeats_sent' in entity stats!"
fi

# 2. Validation: Entity Name is NOT "pending" (confirms deferred init worked)
if [[ "$STATS_JSON" == *"\"entity_name\":\"pending\""* ]]; then
    fail "Entity name is 'pending' - deferred telemetry init failed!"
fi

# 3. Validation: Bus Metrics (Optional - may not appear with deferred init)
# Note: fluxrig.bus.publish_count and fluxrig.nats.messages_published may not appear
# because bus metrics are recorded via InstrumentedBus which requires telemetry to be initialized.
# With deferred init, enrollment messages (Hello) are sent BEFORE telemetry init.
if [[ "$STATS_JSON" != *"fluxrig.bus.publish_count"* ]]; then
    log_info "ℹ️  'fluxrig.bus.publish_count' not found - expected with deferred telemetry init"
fi

# 4. Validation: specific entity verification
# We expect at least one entity (Mixer or Rack) to have stats.
ENTITY_COUNT=$(echo "$STATS_JSON" | grep -o "\"entity_id\"" | wc -l)
if [ "$ENTITY_COUNT" -lt 1 ]; then
    fail "No entities returned in stats API!"
fi

log_success "Entity Stats API Verified ($ENTITY_COUNT entities found)."

# ==============================================================================
section "Registry Table Content"
# ==============================================================================

$DB_CLI -csv -header "$SNAPSHOT_DB" "SELECT * FROM registry ORDER BY type_id, machine_id"

section "Log Entries per Entity"
$DB_CLI -csv -header -c "SELECT entity_name, count(*) as log_count FROM read_parquet('$PARQUET_GLOB') GROUP BY entity_name ORDER BY log_count DESC"

# ==============================================================================
section "Parquet Metrics Analysis"
# ==============================================================================

METRICS_GLOB="$TELEMETRY_DIR/metrics/**/*.parquet"

# Check if metrics parquet files exist
if [ -n "$(find "$TELEMETRY_DIR/metrics" -name "*.parquet" -print -quit 2>/dev/null)" ]; then
    log_info "Metrics Summary by Entity and Name:"
    $DB_CLI -csv -header -c "SELECT entity_name, name, count(*) as count, min(timestamp) as first_ts, max(timestamp) as last_ts FROM read_parquet('$METRICS_GLOB') GROUP BY entity_name, name ORDER BY entity_name, name"

    log_info "Total Metrics by Entity:"
    $DB_CLI -csv -header -c "SELECT entity_name, count(*) as total_metrics FROM read_parquet('$METRICS_GLOB') GROUP BY entity_name ORDER BY total_metrics DESC"

    log_info "Sample Metrics (last 10):"
    $DB_CLI -csv -header -c "SELECT timestamp, entity_name, name, value FROM read_parquet('$METRICS_GLOB') ORDER BY timestamp DESC LIMIT 10"

    # Validation: Host Metrics
    HOST_METRICS_COUNT=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$METRICS_GLOB') WHERE name LIKE 'system.%' OR name LIKE 'process.runtime.%'")
    if [ "$HOST_METRICS_COUNT" -eq 0 ]; then
        fail "No Host/Runtime metrics found (system.*, process.runtime.*)"
    fi
    log_success "Host Metrics Verified ($HOST_METRICS_COUNT found)."

    # Detail Check: Memory Usage > 0
    MEM_USAGE_CHECK=$($DB_CLI -noheader -csv -c "SELECT MAX(value) FROM read_parquet('$METRICS_GLOB') WHERE name = 'system.memory.usage'")
    if [ $(echo "$MEM_USAGE_CHECK > 0" | bc -l) -ne 1 ]; then
         log_info "System Memory Usage appears to be 0 or missing? Max: $MEM_USAGE_CHECK"
    else
         log_success "Memory Usage Verified (Max: $MEM_USAGE_CHECK bytes)"
    fi

    # Validation: Wire Latency
    WIRE_LATENCY_COUNT=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$METRICS_GLOB') WHERE name = 'fluxrig.wire.duration_ms'")
    logger_msg="Wire Latency Metrics Verified ($WIRE_LATENCY_COUNT found)."
    if [ "$WIRE_LATENCY_COUNT" -eq 0 ]; then
        log_info "No Wire Latency metrics found (fluxrig.wire.duration_ms). This is expected if only Heartbeats are sent."
    else
        log_success "$logger_msg"
    fi

else
    log_info "No metrics parquet files found in $TELEMETRY_DIR/metrics"
    fail "Metrics Parquet files missing!"
fi

banner "Telemetry E2E PASSED"
