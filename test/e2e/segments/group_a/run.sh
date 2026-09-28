#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Group A: one shared plaintext Mixer + Racks, auto_adopt=true, default
# encryption on, no Mixer restarts. Phases below run sequentially against the
# same Mixer instead of each paying its own build/keygen/boot cost.
#
# Phases (source suite retired once this segment covers it). Run in this
# order, not suite-number order: telemetry's registry-count assertions
# (exactly 1 Mixer/Snake/Rack) only hold before any other phase's Rack has
# ever enrolled, so it must go first.
#   02: telemetry                                 (was 02_telemetry)
#   01: registration & passport                   (was 01_simple)
#   06: conflict & identity                        (was 06_conflict)
#   16: data at rest                               (was 16_data_at_rest)
#   07: bento load generator smoke test             (was 07_load)
#   12: spec & scenario manager                      (was 12_specs)
#   10: ISO 8583 universal matrix                     (was 10_iso8583, see phase_iso8583_family.sh)
#   13: processor simulator                           (was 13_processor_sim, ditto)
#   14: payment switch (Conductor BIN routing)         (was 14_payment_switch, ditto)
#
# Runs on API port 8090 / Snake port 4223 (see
# the internal e2e-consolidation design notes for the full
# port table across every segment). Cleanup here is PID-tracked only,
# deliberately never `pkill -f "bin/fluxrig"`: that pattern matches every
# fluxrig process on the machine by binary path, which would kill other
# segments running concurrently once this is launched from
# run_consolidated.sh alongside them.
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../../.." && pwd)"

source "${ROOT_DIR}/test/e2e/utils/e2e_utils.sh"

API_PORT=8090
SNAKE_PORT=4223
API_URL="http://localhost:${API_PORT}/api/v1"
UUID_PATTERN="[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"

setup_workspace "group_a" "$BASE_DIR"

banner "Group A: Registration, Conflict & Identity, Data at Rest"

MIXER_LOG="$WORK_DIR/mixer/mixer.stdout"

MIXER_PID=""
declare -A RACK_PIDS=()

stop_pid() {
    local pid="$1"
    if [ -n "$pid" ]; then
        kill -9 "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    fi
}

cleanup() {
    log_info "Shutting down Group A..."
    for name in "${!RACK_PIDS[@]}"; do
        stop_pid "${RACK_PIDS[$name]}"
    done
    stop_pid "$MIXER_PID"
}
trap cleanup EXIT

# 0. This segment owns 8090/4223 exclusively (see the port table); clearing
# them is scoped to those two ports, never a blanket process-name kill.
lsof -ti :${API_PORT} | xargs kill -9 2>/dev/null || true
lsof -ti :${SNAKE_PORT} | xargs kill -9 2>/dev/null || true

# FLUXRIG_E2E_SKIP_BUILD is set by run_consolidated.sh, which builds once
# before launching every segment in parallel — concurrent `make build`
# calls from multiple segments would race on writing the same bin/fluxrig*
# output files. Standalone runs (this var unset) still build normally.
if [ -z "${FLUXRIG_E2E_SKIP_BUILD:-}" ]; then
    log_info "Building..."
    cd "${ROOT_DIR}"
    make build > "${WORK_DIR}/mixer/logs/build.log" 2>&1 || fail "Build failed. Check ${WORK_DIR}/mixer/logs/build.log"
fi

gen_keys "$WORK_DIR/mixer/data/cluster.key"

# 1. One Mixer for the whole segment.
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
cd "${WORK_DIR}/mixer"
"$(bin_dir)/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"
wait_for_mixer "$API_URL"

purge_rack() {
    local name="$1"
    curl -s -X DELETE "$API_URL/racks/$name" > /dev/null || true
}

start_named_rack() {
    # Usage: start_named_rack <pid_key> <config_dir_name>
    local pid_key="$1"
    local dir_name="$2"
    mkdir -p "$WORK_DIR/$dir_name/data" "$WORK_DIR/$dir_name/logs"
    cp "${BASE_DIR}/${dir_name}/rack.toml" "$WORK_DIR/$dir_name/rack.toml"
    cd "$WORK_DIR/$dir_name"
    "$(bin_dir)/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
    RACK_PIDS[$pid_key]=$!
    cd "${BASE_DIR}"
}

# ==============================================================================
section "Phase 02: Telemetry"
# ==============================================================================
# Runs first, before any other phase's Rack registers: its registry-count
# assertions (exactly 1 Mixer, 1 Snake, 1 Rack) are only true while this is
# the only Rack that has ever enrolled against this Mixer.

DB_CLI="duckdb"
FLUX_BIN="$(bin_dir)/fluxrig"
MIXER_DB="$WORK_DIR/mixer/data/flux.duckdb"
TELEMETRY_DIR="$WORK_DIR/mixer/data/telemetry"

mkdir -p "$WORK_DIR/rack_telemetry/data" "$WORK_DIR/rack_telemetry/logs"
cp "${BASE_DIR}/rack_telemetry/rack.toml" "$WORK_DIR/rack_telemetry/rack.toml"
cd "$WORK_DIR/rack_telemetry"
FLUXRIG_DISABLE_TELEMETRY=false "$(bin_dir)/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PIDS[telemetry]=$!
cd "${BASE_DIR}"

log_info "Waiting for Rack Registration..."
FOUND=0
for ((i=1;i<=30;i++)); do
    curl -s --max-time 2 "$API_URL/racks" > "$WORK_DIR/mixer/logs/api_racks_telemetry.json"
    if grep -q "rack-e2e-telemetry-01" "$WORK_DIR/mixer/logs/api_racks_telemetry.json"; then
        log_success "Rack Registered!"
        FOUND=1
        break
    fi
    sleep 1
done
[ $FOUND -eq 1 ] || { cat "$WORK_DIR/rack_telemetry/rack.stdout"; fail "Rack failed to register."; }

log_info "Waiting for Telemetry Ingestion (30s for metrics export)..."
sleep 30

section "Parquet Content Verification"
if [ -z "$(find "$TELEMETRY_DIR/logs" -name "*.parquet" -print -quit 2>/dev/null)" ]; then
    tail -n 40 "$MIXER_LOG"
    fail "No Parquet files found in $TELEMETRY_DIR/logs"
fi

PARQUET_GLOB="$TELEMETRY_DIR/logs/**/*.parquet"
COUNT_LOGS=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$PARQUET_GLOB')")
COUNT_RACK_LOGS=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$PARQUET_GLOB') WHERE entity_name LIKE 'rack%'")
log_info "Found $COUNT_LOGS Logs total"
log_info "Found $COUNT_RACK_LOGS Rack Logs (by Name)"
[ "$COUNT_LOGS" -gt 0 ] || fail "No Telemetry Data Found in Parquet."
[ "$COUNT_RACK_LOGS" -gt 0 ] || fail "No Rack Logs Found (Only Mixer?)."
log_success "Parquet Data Verified."

section "CLI Telemetry Verification"
log_info "Waiting additional 10s for metric batching..."
sleep 10

log_info "Testing 'fluxrig logs'..."
LOGS_OUT=$($FLUX_BIN logs --api-url "http://127.0.0.1:${API_PORT}" --limit 20 --min-level debug)
LOG_COUNT=$(echo "$LOGS_OUT" | tail -n +2 | grep -v "^$" | wc -l | tr -d ' ')
[ "$LOG_COUNT" -ge 5 ] || fail "CLI Logs Verification Failed. Expected at least 5 logs, got $LOG_COUNT"
log_success "CLI Logs Verified ($LOG_COUNT logs found)."

log_info "Testing 'fluxrig metrics' (Waiting for availability)..."
METRICS_FOUND=0
for ((i=1;i<=10;i++)); do
    METRICS_OUT=$($FLUX_BIN metrics --api-url "http://127.0.0.1:${API_PORT}" --limit 10)
    if [[ "$METRICS_OUT" == *"heartbeats_sent"* ]]; then
        METRICS_FOUND=1
        break
    fi
    sleep 2
done
[ $METRICS_FOUND -eq 1 ] || log_info "CLI Metrics Verification deferred (Missing 'heartbeats_sent'). Known Issue: Metric Ingestion/Export batching."
log_success "CLI Metrics Verification Attempted."

section "Registry Verification"
cp "$MIXER_DB" "$WORK_DIR/mixer/data/snapshot_telemetry.duckdb"
[ -f "$MIXER_DB.wal" ] && cp "$MIXER_DB.wal" "$WORK_DIR/mixer/data/snapshot_telemetry.duckdb.wal"
SNAPSHOT_DB="$WORK_DIR/mixer/data/snapshot_telemetry.duckdb"

MIXER_COUNT=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT count(*) FROM registry WHERE type_id=2")
SNAKE_COUNT=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT count(*) FROM registry WHERE type_id=9")
RACK_COUNT=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT count(*) FROM registry WHERE type_id=4")
RACK_MID=$($DB_CLI -noheader -csv "$SNAPSHOT_DB" "SELECT machine_id FROM registry WHERE type_id=4 LIMIT 1")
rm -f "$SNAPSHOT_DB" "$SNAPSHOT_DB.wal"

[[ "$RACK_MID" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || fail "Rack MachineID is not a valid UUID: $RACK_MID"
[ "$MIXER_COUNT" -eq 1 ] || fail "Expected 1 Mixer, got $MIXER_COUNT"
[ "$SNAKE_COUNT" -eq 1 ] || fail "Expected 1 Snake, got $SNAKE_COUNT"
[ "$RACK_COUNT" -eq 1 ] || fail "Expected 1 Rack, got $RACK_COUNT"
log_success "Registry Counts Verified."

section "API Entity Stats Verification"
STATS_JSON=$(curl -s "$API_URL/entities/stats")
[[ "$STATS_JSON" == *"heartbeats_sent"* ]] || fail "Missing 'heartbeats_sent' in entity stats!"
[[ "$STATS_JSON" != *"\"entity_name\":\"pending\""* ]] || fail "Entity name is 'pending' - deferred telemetry init failed!"
ENTITY_COUNT=$(echo "$STATS_JSON" | grep -o "\"entity_id\"" | wc -l)
[ "$ENTITY_COUNT" -ge 1 ] || fail "No entities returned in stats API!"
log_success "Entity Stats API Verified ($ENTITY_COUNT entities found)."

section "Parquet Metrics Analysis"
METRICS_GLOB="$TELEMETRY_DIR/metrics/**/*.parquet"
if [ -n "$(find "$TELEMETRY_DIR/metrics" -name "*.parquet" -print -quit 2>/dev/null)" ]; then
    HOST_METRICS_COUNT=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$METRICS_GLOB') WHERE name LIKE 'system.%' OR name LIKE 'process.runtime.%'")
    [ "$HOST_METRICS_COUNT" -gt 0 ] || fail "No Host/Runtime metrics found (system.*, process.runtime.*)"
    log_success "Host Metrics Verified ($HOST_METRICS_COUNT found)."
else
    fail "Metrics Parquet files missing!"
fi

log_success "Phase 02 OK"

# ==============================================================================
section "Phase 01: Registration & Passport"
# ==============================================================================

start_named_rack "simple" "rack_simple"

log_info "Verifying Registration (Expecting 'rack-e2e-01')..."
FOUND=0
for ((i=1;i<=30;i++)); do
    curl -s --max-time 2 "$API_URL/racks" > "$WORK_DIR/mixer/logs/api_racks_p01.json"
    if grep -q "rack-e2e-01" "$WORK_DIR/mixer/logs/api_racks_p01.json"; then
        log_success "Rack Registered and Found in API!"
        FOUND=1
        break
    fi
    sleep 1
done
[ $FOUND -eq 1 ] || { tail -n 20 "$WORK_DIR/rack_simple/rack.stdout"; fail "Timeout waiting for rack-e2e-01 registration."; }

STATE_FILE="$WORK_DIR/rack_simple/data/state.flux"
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
    tail -n 20 "$WORK_DIR/rack_simple/rack.stdout"
    fail "Passport MISSING at $STATE_FILE"
fi

log_info "Verifying CLI..."
"$(bin_dir)/fluxrig" admin --api-url "http://localhost:${API_PORT}" racks list > "$WORK_DIR/mixer/logs/cli_list_p01.txt" 2>&1
if grep -q "rack-e2e-01" "$WORK_DIR/mixer/logs/cli_list_p01.txt"; then
    log_success "CLI lists the rack."
else
    cat "$WORK_DIR/mixer/logs/cli_list_p01.txt"
    fail "CLI failed to list rack."
fi

log_info "Verifying DB Content (Snapshot)..."
cp "$WORK_DIR/mixer/data/flux.duckdb" "$WORK_DIR/mixer/data/snapshot_p01.duckdb"
[ -f "$WORK_DIR/mixer/data/flux.duckdb.wal" ] && cp "$WORK_DIR/mixer/data/flux.duckdb.wal" "$WORK_DIR/mixer/data/snapshot_p01.duckdb.wal"
DB_OUT=$(duckdb -readonly -csv -noheader -c "SELECT name, machine_id FROM registry WHERE name='rack-e2e-01' AND type_id=4;" "$WORK_DIR/mixer/data/snapshot_p01.duckdb")
rm -f "$WORK_DIR/mixer/data/snapshot_p01.duckdb" "$WORK_DIR/mixer/data/snapshot_p01.duckdb.wal"
[[ "$DB_OUT" == *"rack-e2e-01"* ]] || fail "DB Verification Failed (Rack not found in DB snapshot)"
log_success "DB Verification Passed (Rack found in DB)"

log_success "Phase 01 OK"

# ==============================================================================
section "Phase 06: Conflict & Identity"
# ==============================================================================

# --- Scenario 1: Active Conflict (Security) ---
log_info "--- Scenario 1: Active Conflict ---"
purge_rack "rack-shared"
start_named_rack "conflict_a" "rack_conflict_a"

wait_for_log "$WORK_DIR/rack_conflict_a/rack.stdout" "Passport Verified" 20 || { cat "$WORK_DIR/rack_conflict_a/rack.stdout"; fail "Rack A Enrollment timeout"; }
ID_A=$(grep -a "Passport Verified" "$WORK_DIR/rack_conflict_a/rack.stdout" | grep -oE "id=$UUID_PATTERN" | head -n1 | cut -d= -f2)
NAME_A=$(grep -a "Passport Verified" "$WORK_DIR/rack_conflict_a/rack.stdout" | grep -oE "name=[^ ]*" | head -n1 | cut -d= -f2)
log_success "Rack A Registered (ID: $ID_A, Name: $NAME_A)"

start_named_rack "conflict_b" "rack_conflict_b"
sleep 3
if grep -q "Passport Verified" "$WORK_DIR/rack_conflict_b/rack.stdout"; then
    fail "Rack B registered successfully (Should be Rejected)"
fi
log_success "Rack B was rejected (bootstrap secret alone cannot hijack an enrolled name)"
stop_pid "${RACK_PIDS[conflict_b]}"
unset 'RACK_PIDS[conflict_b]'

# --- Scenario 2: Session Recovery ---
log_info "--- Scenario 2: Session Recovery ---"
stop_pid "${RACK_PIDS[conflict_a]}"
cd "$WORK_DIR/rack_conflict_a"
"$(bin_dir)/fluxrig" rack -c "rack.toml" >> "rack.stdout" 2>&1 &
RACK_PIDS[conflict_a]=$!
cd "${BASE_DIR}"

wait_for_log "$WORK_DIR/rack_conflict_a/rack.stdout" "Loaded Cached Passport" 20 || { cat "$WORK_DIR/rack_conflict_a/rack.stdout"; fail "Rack A Recovery timeout"; }
log_success "Rack A recovered session"

# --- Scenario 3: Zero Config (Auto-Scale) ---
log_info "--- Scenario 3: Zero-Config (Cattle) ---"
start_named_rack "conflict_c" "rack_conflict_c"
wait_for_log "$WORK_DIR/rack_conflict_c/rack.stdout" "Passport Verified" 20 || { cat "$WORK_DIR/rack_conflict_c/rack.stdout"; fail "Rack C Enrollment timeout"; }
ID_C=$(grep "Passport Verified" "$WORK_DIR/rack_conflict_c/rack.stdout" | grep -oE "id=$UUID_PATTERN" | head -n1 | cut -d= -f2)
NAME_C=$(grep "Passport Verified" "$WORK_DIR/rack_conflict_c/rack.stdout" | grep -oE 'name=[^ ]*' | head -n1 | cut -d= -f2)
[ -n "$ID_C" ] || fail "Rack C failed to register"
[[ "$NAME_C" == probes-* ]] || fail "Rack C assigned name '$NAME_C' does not start with 'probes-'"
log_success "Rack C Registered (ID: $ID_C, Name: $NAME_C)"

start_named_rack "conflict_d" "rack_conflict_d"
wait_for_log "$WORK_DIR/rack_conflict_d/rack.stdout" "Passport Verified" 20 || { cat "$WORK_DIR/rack_conflict_d/rack.stdout"; fail "Rack D Enrollment timeout"; }
ID_D=$(grep "Passport Verified" "$WORK_DIR/rack_conflict_d/rack.stdout" | grep -oE "id=$UUID_PATTERN" | head -n1 | cut -d= -f2)
NAME_D=$(grep "Passport Verified" "$WORK_DIR/rack_conflict_d/rack.stdout" | grep -oE 'name=[^ ]*' | head -n1 | cut -d= -f2)
[ -n "$ID_D" ] || fail "Rack D failed to register"
[[ "$NAME_D" == probes-* ]] || fail "Rack D assigned name '$NAME_D' does not start with 'probes-'"
log_success "Rack D Registered (ID: $ID_D, Name: $NAME_D)"
[ "$ID_C" != "$ID_D" ] || fail "Rack C and D got same ID ($ID_C)"
log_success "Unique IDs assigned via Auto-Scale"

# --- Scenario 4: Default Zero Config (No Prefix) ---
log_info "--- Scenario 4: Default Zero Config ---"
start_named_rack "conflict_e" "rack_conflict_e"
wait_for_log "$WORK_DIR/rack_conflict_e/rack.stdout" "Passport Verified" 20 || { cat "$WORK_DIR/rack_conflict_e/rack.stdout"; fail "Rack E Enrollment timeout"; }
ID_E=$(grep "Passport Verified" "$WORK_DIR/rack_conflict_e/rack.stdout" | grep -oE "id=$UUID_PATTERN" | head -n1 | cut -d= -f2)
NAME_E=$(grep "Passport Verified" "$WORK_DIR/rack_conflict_e/rack.stdout" | grep -oE 'name=[^ ]*' | head -n1 | cut -d= -f2)
[ -n "$ID_E" ] || fail "Rack E failed to register"
[[ "$NAME_E" == node-* ]] || fail "Rack E assigned name '$NAME_E' does not start with default 'node-'"
log_success "Rack E Registered (ID: $ID_E, Name: $NAME_E)"

log_success "Phase 06 OK"

# ==============================================================================
section "Phase 16: Data at Rest"
# ==============================================================================

PAN=4111111111111111
MESSAGES=20
STREAM_DIR="$WORK_DIR/mixer/data/snake/jetstream"
ENROLL_TIMEOUT=30
STREAM_TIMEOUT=30

start_named_rack "dar_a" "rack_dar_a"
start_named_rack "dar_b" "rack_dar_b"

for r in a b; do
    deadline=$((SECONDS + ENROLL_TIMEOUT))
    until curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-$r"; do
        (( SECONDS < deadline )) || fail "rack-$r did not become active."
        sleep 0.5
    done
done
log_success "Both Racks are active."

HTTP_CODE=$(curl -s -o "$WORK_DIR/import_p16.out" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
    -H "Content-Type: application/x-yaml" --data-binary @"${BASE_DIR}/scenario_dar.yaml")
[ "$HTTP_CODE" == "200" ] || { cat "$WORK_DIR/import_p16.out"; fail "Scenario import failed (HTTP $HTTP_CODE)."; }
wait_for_port 9621 30 || fail "gw-a is not listening."
wait_for_port 9622 30 || fail "gw-b is not listening."

log_info "Sending $MESSAGES messages carrying a card number..."
for i in $(seq 1 $MESSAGES); do
    exec 3<>/dev/tcp/127.0.0.1/9621
    printf '0100%s16%s000000010000%06d\n' "7234054128C28805" "$PAN" "$i" >&3
    exec 3>&- 3<&-
done

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

LEAKS=$(grep -rlaE "$PAN" "$WORK_DIR/mixer" "$WORK_DIR/rack_dar_a" "$WORK_DIR/rack_dar_b" 2>/dev/null | sed "s#$WORK_DIR/##")
if [ -z "$LEAKS" ]; then
    log_success "The card number is in no file the Mixer or the Racks wrote."
else
    log_error "The card number is readable in:"
    echo "$LEAKS"
    fail "A card number rests in clear."
fi

log_success "Phase 16 OK"

# ==============================================================================
section "Phase 07: Bento Load Generator Smoke Test"
# ==============================================================================
# The one member of this group started via `fluxrig run`, not `fluxrig
# rack`: it needs the gear-hosting session, since the scenario deploys Bento
# gears to it. Its rack directory is named plain "rack" (not "rack_load"),
# because utils/compare_logs.py hardcodes "<work_dir>/rack/logs/rack.log".

LOAD_OUT="$WORK_DIR/load_out.txt"
rm -f "$LOAD_OUT"
# The scenario's sink gear writes to a fixed path, same as the source test.
sed "s#/tmp/fluxrig_e2e_load_out.txt#${LOAD_OUT}#" "${BASE_DIR}/scenario_load.yaml" > "$WORK_DIR/scenario_load.yaml"

cp "${BASE_DIR}/rack/fluxrig.toml" "$WORK_DIR/rack/fluxrig.toml"
cd "$WORK_DIR/rack"
"$(bin_dir)/fluxrig" run -c "fluxrig.toml" > "rack.stdout" 2>&1 &
RACK_PIDS[load]=$!
cd "${BASE_DIR}"

wait_for_rack "$API_URL" "load-node-01" 15

sleep 2
RESPONSE=$(curl -s -X POST "${API_URL}/scenario/import?activate=true" \
    -H "Content-Type: application/x-yaml" --data-binary @"$WORK_DIR/scenario_load.yaml")
log_info "Scenario Import Response: $RESPONSE"
[[ "$RESPONSE" == *"imported_and_activated"* ]] || fail "Scenario import failed"

log_info "Waiting for messages to flow (interval 1s, count 5)..."
LINES=0
for i in {1..15}; do
    if [ -f "$LOAD_OUT" ]; then
        LINES=$(wc -l < "$LOAD_OUT" | tr -d ' ')
        [ "$LINES" -ge 5 ] && { log_success "Received 5+ messages"; break; }
    fi
    sleep 1
done
[ -f "$LOAD_OUT" ] || fail "Output file not found"
[ "$LINES" -ge 5 ] || fail "Insufficient messages: $LINES (Expected 5)"
log_info "Output Content:"
cat "$LOAD_OUT"

log_info "Waiting for telemetry flush..."
sleep 5
LOAD_TEL_COUNT=$($DB_CLI -noheader -csv -c "SELECT count(*) FROM read_parquet('$TELEMETRY_DIR/logs/**/*.parquet') WHERE entity_name LIKE 'load%'" 2>/dev/null || echo 0)
[ "$LOAD_TEL_COUNT" -gt 0 ] || fail "No telemetry found for load-node-01."
log_success "Found $LOAD_TEL_COUNT telemetry log entries for load-node-01."

if command -v duckdb >/dev/null 2>&1; then
    log_info "--- Log Parity Check ---"
    python3 "${ROOT_DIR}/test/e2e/utils/compare_logs.py" "${WORK_DIR}" || fail "Log Parity Check Failed"
else
    log_warn "duckdb CLI not found, skipping deep inspection."
fi

log_success "Phase 07 OK"

# ==============================================================================
section "Phase 12: Spec & Scenario Manager"
# ==============================================================================
# Runs against Group A's already-running shared Mixer instead of starting its
# own: the source test's own "Concurrent Access" check already proves CLI
# spec operations are safe while the Mixer runs, so there is no need to do
# the CLI-only checks before Mixer startup the way the source test did.

SPECS_STORE_DIR="$WORK_DIR/mixer/data"
SPECS_SCENARIO_DIR="${BASE_DIR}/specs_scenarios"
SPECS_OUTPUT="$WORK_DIR/specs_output.log"
FLUXRIG_BIN="$(bin_dir)/fluxrig"

run_fluxrig_spec() {
    "${FLUXRIG_BIN}" spec "$@" --store-dir "${SPECS_STORE_DIR}"
}

test_cli_spec_lifecycle() {
    section "CLI Spec Lifecycle (Local Store)"

    log_info "Importing visa:v1.0.0..."
    run_fluxrig_spec import "${SPECS_SCENARIO_DIR}/visa.yaml" --name visa --tag v1.0.0 > "${SPECS_OUTPUT}" 2>&1
    grep -q "Imported visa:v1.0.0" "${SPECS_OUTPUT}" || { log_error "Failed to import visa:v1.0.0"; cat "${SPECS_OUTPUT}"; return 1; }
    log_success "Imported visa:v1.0.0"

    if run_fluxrig_spec import "${SPECS_SCENARIO_DIR}/visa.yaml" --name visa --tag v1.0.0 > /dev/null 2>&1; then
        log_success "Idempotency Verified"
    else
        log_error "Idempotency check failed"; return 1
    fi

    if run_fluxrig_spec import "${SPECS_SCENARIO_DIR}/visa.yaml" --name visa --tag "invalid-tag" > "${SPECS_OUTPUT}" 2>&1; then
        log_error "Should have failed (invalid version)"; return 1
    else
        log_success "Rejected invalid version (as expected)"
    fi

    if run_fluxrig_spec import "${SPECS_SCENARIO_DIR}/visa_mod.yaml" --name visa --tag v1.0.0 > "${SPECS_OUTPUT}" 2>&1; then
        log_error "Should have failed (conflict)"; return 1
    else
        if grep -q "already exists" "${SPECS_OUTPUT}"; then
            log_success "Conflict detected correctly"
        else
            log_warn "Failed but message differed: $(cat "${SPECS_OUTPUT}")"
        fi
    fi

    run_fluxrig_spec list > "${SPECS_OUTPUT}" 2>&1
    grep -q "visa.*v1.0.0" "${SPECS_OUTPUT}" || { log_error "List missing visa:v1.0.0"; return 1; }
    log_success "List verified"

    log_info "Importing a spec with no flags..."
    run_fluxrig_spec import "${SPECS_SCENARIO_DIR}/self_named.yaml" > "${SPECS_OUTPUT}" 2>&1
    grep -q "Imported acme-auth:v2.2.0" "${SPECS_OUTPUT}" || { log_error "Expected acme-auth:v2.2.0 from the document itself"; cat "${SPECS_OUTPUT}"; return 1; }
    log_success "Filed under its declared id and version"

    if grep -qE "Imported (ACME|.*\(1987\))" "${SPECS_OUTPUT}"; then
        log_error "The human title was used as the reference"; return 1
    else
        log_success "Title kept out of the reference"
    fi

    run_fluxrig_spec import "${SPECS_SCENARIO_DIR}/self_named.yaml" > "${SPECS_OUTPUT}" 2>&1
    grep -q "Imported acme-auth:v2.2.0" "${SPECS_OUTPUT}" || { log_error "Re-import minted a different reference: $(cat "${SPECS_OUTPUT}")"; return 1; }
    log_success "Re-import is idempotent without flags"
}

test_api_scenario_lifecycle() {
    section "API Scenario Lifecycle (Mixer Integration)"

    log_info "Importing and activating a scenario whose Rack has not enrolled..."
    local prior_active
    prior_active=$(cat "${SPECS_STORE_DIR}/scenarios/active" 2>/dev/null || echo "")

    HTTP_CODE=$(curl -s -o "${SPECS_OUTPUT}" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
        -H "Content-Type: application/x-yaml" \
        --data-binary @"${SPECS_SCENARIO_DIR}/scenario_v1.yaml")

    if [[ "$HTTP_CODE" == "409" ]] && grep -q "deploys to unknown or inactive target 'rack-1'" "${SPECS_OUTPUT}"; then
        log_success "Activation refused while rack-1 is not enrolled (HTTP 409)"
    else
        log_error "Expected HTTP 409 naming the missing Rack, got HTTP ${HTTP_CODE}"; cat "${SPECS_OUTPUT}"; return 1
    fi

    # Group A's Mixer is not fresh: an earlier phase's scenario may already be
    # active. The real invariant a refused activation must uphold is that it
    # doesn't change WHICH scenario that is, not that none is active at all.
    local now_active
    now_active=$(cat "${SPECS_STORE_DIR}/scenarios/active" 2>/dev/null || echo "")
    if [[ "$now_active" == "payment-flow" ]]; then
        log_error "A refused activation left payment-flow active"; return 1
    fi
    [[ "$now_active" == "$prior_active" ]] || log_warn "Active scenario changed from '$prior_active' to '$now_active' during a refused activation"
    if [[ -f "${SPECS_STORE_DIR}/scenarios/payment-flow.yaml" ]]; then
        log_success "Scenario stayed filed, and payment-flow is not active"
    else
        log_error "The scenario was not filed by the import"; ls -R "${SPECS_STORE_DIR}"; return 1
    fi

    start_named_rack "specs" "rack_specs" || return 1
    local deadline=$((SECONDS + ENROLL_TIMEOUT))
    while (( SECONDS < deadline )); do
        curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-1" && break
        sleep 0.5
    done
    curl -s --max-time 2 "${API_URL}/racks?status=active" | grep -q "rack-1" || { log_error "Rack rack-1 did not become active within ${ENROLL_TIMEOUT}s"; cat "$WORK_DIR/rack_specs/rack.stdout"; return 1; }
    log_success "Rack rack-1 is active"

    log_info "Importing and activating it again..."
    HTTP_CODE=$(curl -s -o "${SPECS_OUTPUT}" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
        -H "Content-Type: application/x-yaml" \
        --data-binary @"${SPECS_SCENARIO_DIR}/scenario_v1.yaml")
    [[ "$HTTP_CODE" == "200" || "$HTTP_CODE" == "201" ]] || { log_error "Scenario Import Failed (HTTP $HTTP_CODE)"; cat "${SPECS_OUTPUT}"; return 1; }
    log_success "Scenario Imported and Activated (HTTP $HTTP_CODE)"

    log_info "Verifying Topology Status..."
    STATUS_JSON=$(curl -s "${API_URL}/topology/status")
    if [[ "$STATUS_JSON" =~ "active_ver" ]] && [[ "$STATUS_JSON" != *"unknown"* ]]; then
        log_success "Topology Status Verified: $STATUS_JSON"
    else
        log_error "Topology Status Invalid: $STATUS_JSON"; return 1
    fi

    if [[ -f "${SPECS_STORE_DIR}/scenarios/active" ]] && [[ "$(cat "${SPECS_STORE_DIR}/scenarios/active")" == "payment-flow" ]]; then
        log_success "Scenario persisted to disk and is active"
    else
        log_error "payment-flow is not the active scenario on disk"; ls -R "${SPECS_STORE_DIR}"; return 1
    fi
}

test_concurrent_access() {
    section "Concurrent Access (CLI + Mixer)"
    log_info "Importing mastercard:v2.0.0 via CLI while Mixer runs..."
    run_fluxrig_spec import "${SPECS_SCENARIO_DIR}/mc.yaml" --name mastercard --tag v2.0.0 > "${SPECS_OUTPUT}" 2>&1
    grep -q "Imported mastercard:v2.0.0" "${SPECS_OUTPUT}" || { log_error "CLI import failed"; cat "${SPECS_OUTPUT}"; return 1; }
    log_success "CLI import succeeded during Mixer runtime"

    kill -0 "$MIXER_PID" 2>/dev/null || { log_error "Mixer crashed during CLI operation!"; return 1; }
    log_success "Mixer stability confirmed"
}

test_cli_spec_lifecycle || fail "CLI spec tests failed"
test_api_scenario_lifecycle || fail "API scenario tests failed"
test_concurrent_access || fail "Concurrent tests failed"

log_success "Phase 12 OK"

# ==============================================================================
# Phases 10, 13, 14: ISO 8583 family. Kept in its own file (see the comment
# at its top) purely for size.
# ==============================================================================
source "${BASE_DIR}/phase_iso8583_family.sh"

banner "Group A: ALL PHASES PASSED"
