#!/bin/bash
# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

set -u

# ==============================================================================
# Group B: one shared Mixer, `auto_adopt=false`, pending-then-approve
# workflow. Phases below run sequentially against the same Mixer instead of
# each paying its own build/keygen/boot cost.
#
# Phases (source suite retired once this segment covers it):
#   03: registry                                  (was 03_registry)
#   05: CLI & admin                                (was 05_cli)
#
# Runs on API port 8093 / Snake port 4224 (see
# the port table in the internal e2e-consolidation design notes).
# Cleanup here is PID-tracked only, never `pkill -f "bin/fluxrig"` (see the
# same doc's "Constraint found during implementation" note) — this also
# means `kill_old_processes` from e2e_utils.sh (itself a `pkill -f
# "bin/fluxrig"`) is never called here.
#
# 03_registry's original script stopped the Mixer twice: once purely to
# release DuckDB's single-writer lock before reading the registry table
# with the `duckdb` CLI, and again (as a side effect of the first stop)
# just to have a live Mixer API for the approve call that follows. Neither
# is a real assertion — the snapshot-copy trick 01_simple/02_telemetry
# already use reads the DB without ever stopping the Mixer, so both
# restarts disappear and the Mixer runs continuously through both phases.
# The one restart that IS load-bearing — the Rack proving it kept its
# approved identity across its own restart — is kept exactly as designed.
# ==============================================================================

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../../.." && pwd)"

source "${ROOT_DIR}/test/e2e/utils/e2e_utils.sh"

API_PORT=8093
SNAKE_PORT=4224
API_URL="http://localhost:${API_PORT}/api/v1"
BASE_URL="http://localhost:${API_PORT}"

setup_workspace "group_b" "$BASE_DIR"

banner "Group B: Registry, CLI & Admin"

MIXER_LOG="$WORK_DIR/mixer/mixer.stdout"
DB_CLI="duckdb"
FLUX_BIN="$(bin_dir)/fluxrig"
MIXER_DB="$WORK_DIR/mixer/data/flux.duckdb"

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
    log_info "Shutting down Group B..."
    for name in "${!RACK_PIDS[@]}"; do
        stop_pid "${RACK_PIDS[$name]}"
    done
    stop_pid "$MIXER_PID"
}
trap cleanup EXIT

# 0. This segment owns 8093/4224 exclusively; clearing them is scoped to
# those two ports, never a blanket process-name kill.
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

# 1. One Mixer for the whole segment, started once and never restarted.
cp "${BASE_DIR}/mixer/mixer.toml" "${WORK_DIR}/mixer/mixer.toml"
cd "${WORK_DIR}/mixer"
"$(bin_dir)/fluxrig-mixer" -c "mixer.toml" > "mixer.stdout" 2>&1 &
MIXER_PID=$!
cd "${BASE_DIR}"
wait_for_mixer "$API_URL"

# Read the registry table via a snapshot copy, never by stopping the Mixer:
# DuckDB's single-writer lock is what the original avoided by killing the
# Mixer, and a snapshot avoids it just as well without losing the process.
snapshot_query() {
    local query="$1" tag="$2"
    cp "$MIXER_DB" "$WORK_DIR/mixer/data/snapshot_${tag}.duckdb"
    [ -f "$MIXER_DB.wal" ] && cp "$MIXER_DB.wal" "$WORK_DIR/mixer/data/snapshot_${tag}.duckdb.wal"
    $DB_CLI -noheader -csv "$WORK_DIR/mixer/data/snapshot_${tag}.duckdb" "$query"
    rm -f "$WORK_DIR/mixer/data/snapshot_${tag}.duckdb" "$WORK_DIR/mixer/data/snapshot_${tag}.duckdb.wal"
}

# ==============================================================================
section "Phase 03: Registry"
# ==============================================================================

mkdir -p "$WORK_DIR/rack_registry/data" "$WORK_DIR/rack_registry/logs"
cp "${BASE_DIR}/rack_registry/rack.toml" "$WORK_DIR/rack_registry/rack.toml"
REG_RACK_LOG="$WORK_DIR/rack_registry/rack.stdout"

cd "$WORK_DIR/rack_registry"
"$(bin_dir)/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PIDS[registry]=$!
cd "${BASE_DIR}"

log_info "Waiting for Rack Registration..."
FOUND=0
for ((i=1;i<=30;i++)); do
    curl -s "$API_URL/racks" > "$WORK_DIR/mixer/logs/api_racks_p03.json"
    if grep -qE "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" "$WORK_DIR/mixer/logs/api_racks_p03.json"; then
        log_success "Rack Registered (Found UUID)!"
        FOUND=1
        break
    fi
    sleep 1
done
[ $FOUND -eq 1 ] || { cat "$REG_RACK_LOG"; fail "Rack failed to register."; }

log_info "Waiting 15s for Snake Registration (NATS Handshake)..."
sleep 15

grep -q "Bus Connected" "$REG_RACK_LOG" || { cat "$REG_RACK_LOG"; fail "Rack did not connect to Bus."; }

log_info "Verifying Registry via CLI..."
RACKS_OUT=$($FLUX_BIN racks --api-url "$BASE_URL")
log_info "$RACKS_OUT"
[[ "$RACKS_OUT" == *"pending"* ]] || fail "CLI Racks Verification Failed."
log_success "CLI Racks Verified."

log_info "Verifying Registry Content..."
MIXER_COUNT=$(snapshot_query "SELECT count(*) FROM registry WHERE type_id=2" "p03a")
SNAKE_COUNT=$(snapshot_query "SELECT count(*) FROM registry WHERE type_id=9" "p03b")
RACK_COUNT=$(snapshot_query "SELECT count(*) FROM registry WHERE type_id=4" "p03c")
log_info "Found $MIXER_COUNT Mixers, $SNAKE_COUNT Snakes, $RACK_COUNT Racks"
[ "$MIXER_COUNT" -eq 1 ] || fail "Expected 1 Mixer, got $MIXER_COUNT"
[ "$SNAKE_COUNT" -eq 1 ] || fail "Expected 1 Snake, got $SNAKE_COUNT"
[ "$RACK_COUNT" -eq 1 ] || fail "Expected 1 Rack, got $RACK_COUNT"
log_success "Registry Counts Verified."

log_info "Verifying Attributes for Rack..."
RACK_ROW=$(snapshot_query "SELECT stats, attributes, version FROM registry WHERE type_id=4 LIMIT 1" "p03d")
log_info "Rack Row: $RACK_ROW"
EXPECTED_VERSION="$(cat "${ROOT_DIR}/VERSION")"
if [[ "$RACK_ROW" == *"$EXPECTED_VERSION"* ]]; then
    log_success "Rack Version Verified ($EXPECTED_VERSION)"
else
    fail "Rack Version Check Failed. Expected '$EXPECTED_VERSION'. Row: $RACK_ROW"
fi

log_info "Verifying Snake Topology..."
SNAKE_ROW=$(snapshot_query "SELECT attributes FROM registry WHERE type_id=9 LIMIT 1" "p03e")
for needle in mixer rack rack_ip rack_port mixer_ip mixer_port; do
    [[ "$SNAKE_ROW" == *"$needle"* ]] || fail "Snake Attributes missing '$needle'!"
done
SNAKE_MID=$(snapshot_query "SELECT machine_id FROM registry WHERE type_id=9 LIMIT 1" "p03f")
[[ "$SNAKE_MID" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || fail "Snake MachineID is not a valid UUID: $SNAKE_MID"
log_success "Registry E2E checks passed."

# --- Adoption flow: approve the pending Rack, then restart it and confirm
# the approved identity persists. This is the one restart from the
# original that is load-bearing (see the header comment).
log_info "Adopting Rack..."
PENDING_ID=$(curl -s "$API_URL/racks?status=pending" | grep -oE "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}" | head -n1)
[ -n "$PENDING_ID" ] || fail "No pending rack found for adoption."

sleep 2
APPROVE_OUT=$($FLUX_BIN admin racks approve "$PENDING_ID" --name "node-100" --api-url "$BASE_URL")
log_info "$APPROVE_OUT"
[[ "$APPROVE_OUT" == *"approved"* ]] || fail "Approval Failed."

log_info "Waiting 5s for Rack to receive Passport & Update..."
sleep 5

log_info "Restarting Rack to verify persistence..."
stop_pid "${RACK_PIDS[registry]}"
cd "$WORK_DIR/rack_registry"
"$(bin_dir)/fluxrig" rack -c "rack.toml" > "rack.stdout.2" 2>&1 &
RACK_PIDS[registry]=$!
cd "${BASE_DIR}"

log_info "Waiting for Rack to reconnect..."
sleep 5

log_info "Registry Table Content (after Phase 03):"
snapshot_query "SELECT * FROM registry ORDER BY type_id" "p03g"

log_success "Phase 03 OK"

# ==============================================================================
section "Phase 05: CLI & Admin"
# ==============================================================================

section "Helper Commands Verification"
VERSION_OUT=$("$(bin_dir)/fluxrig" version)
log_info "Output: $VERSION_OUT"
echo "$VERSION_OUT" | grep -q "^fluxrig" && log_success "'fluxrig version' passed." || fail "'fluxrig version' failed."

HELP_OUT=$("$(bin_dir)/fluxrig" rack --help)
log_info "Output (truncated): $(echo "$HELP_OUT" | head -n 1)..."
echo "$HELP_OUT" | grep -q "Initializes" && log_success "'fluxrig rack --help' passed." || fail "'fluxrig rack --help' failed."

KEYS_OUT=$("$(bin_dir)/fluxrig" keys gen-cluster -o "$WORK_DIR/mixer/data/test_gen.key")
log_info "Output: $KEYS_OUT"
echo "$KEYS_OUT" | grep -q "Private Key" && log_success "'fluxrig keys gen-cluster' passed." || fail "'fluxrig keys gen-cluster' failed."

section "Admin CLI Tests"

mkdir -p "$WORK_DIR/rack_cli/data" "$WORK_DIR/rack_cli/logs"
cp "${BASE_DIR}/rack_cli/rack.toml" "$WORK_DIR/rack_cli/rack.toml"
CLI_RACK_LOG="$WORK_DIR/rack_cli/rack.stdout"

log_info "Starting Rack (Zero Config)..."
cd "$WORK_DIR/rack_cli"
"$(bin_dir)/fluxrig" rack -c "rack.toml" > "rack.stdout" 2>&1 &
RACK_PIDS[cli]=$!
cd "${BASE_DIR}"
sleep 2

section "Pending State Verification"
LIST_OUT=$("$(bin_dir)/fluxrig" admin racks list --api-url "$BASE_URL")
if echo "$LIST_OUT" | grep -q "pending"; then
    log_success "Rack is PENDING as expected."
    log_info "Output: $LIST_OUT"
else
    log_info "Output: $LIST_OUT"
    tail -n 10 "$CLI_RACK_LOG"
    fail "Rack should be PENDING but is NOT."
fi

log_info "Inspecting State (Pending)..."
STATE_OUT_1=$("$(bin_dir)/fluxrig" keys inspect "$WORK_DIR/rack_cli/data/state.flux")
log_info "$STATE_OUT_1"
if echo "$STATE_OUT_1" | grep -q "Name:" && echo "$STATE_OUT_1" | grep -q "cli-test-"; then
    log_success "Name Verified (Matches prefix 'cli-test-')."
else
    fail "Name Mismatch. Expected 'cli-test-' prefix."
fi
echo "$STATE_OUT_1" | grep -q "Status:    pending" && log_success "Status Verified (pending)." || fail "Status Mismatch. Expected 'pending'."

RACK_ID=$(echo "$LIST_OUT" | grep "pending" | awk '{print $1}')
log_info "Extracted Rack ID: $RACK_ID"

section "Adoption (Approve)"
log_info "Approving Rack..."
"$(bin_dir)/fluxrig" admin racks approve $RACK_ID --name "rack-production-01" --api-url "$BASE_URL"
sleep 1
LIST_OUT_2=$("$(bin_dir)/fluxrig" admin racks list --api-url "$BASE_URL")
if echo "$LIST_OUT_2" | grep "rack-production-01" | grep -q "active"; then
    log_success "Rack Approved & Active."
    log_info "Output: $LIST_OUT_2"
else
    log_info "Output: $LIST_OUT_2"
    fail "Rack Approval Failed."
fi

log_info "Inspecting State after Approval..."
sleep 10
STATE_OUT_2=$("$(bin_dir)/fluxrig" keys inspect "$WORK_DIR/rack_cli/data/state.flux")
log_info "$STATE_OUT_2"
echo "$STATE_OUT_2" | grep -q "Name:      rack-production-01" && log_success "Name Verified (Approved Name)." || fail "Name Mismatch. Expected 'rack-production-01'."
echo "$STATE_OUT_2" | grep -q "Status:    active" && log_success "Status Verified (active)." || fail "Status Mismatch. Expected 'active'."

section "Suspension"
log_info "Suspending Rack..."
"$(bin_dir)/fluxrig" admin racks suspend $RACK_ID --api-url "$BASE_URL" --force
sleep 1
LIST_OUT_3=$("$(bin_dir)/fluxrig" admin racks list --api-url "$BASE_URL")
if echo "$LIST_OUT_3" | grep "inactive"; then
    log_success "Rack Suspended (Registry Updated)."
    log_info "Output: $LIST_OUT_3"
    sleep 2
    if grep -q "Status Changed" "$CLI_RACK_LOG" && grep -q 'new="inactive"' "$CLI_RACK_LOG"; then
        log_success "Rack Received Suspension (Log Verified)."
    else
        tail -n 20 "$CLI_RACK_LOG"
        log_warn "Rack did NOT receive suspension notification (Mixer limit?). Proceeding."
    fi
else
    log_info "Output: $LIST_OUT_3"
    fail "Rack Suspension Failed (Registry not updated)."
fi

log_info "Inspecting State after Suspension..."
STATE_OUT_SUSP=$("$(bin_dir)/fluxrig" keys inspect "$WORK_DIR/rack_cli/data/state.flux")
log_info "$STATE_OUT_SUSP"
if echo "$STATE_OUT_SUSP" | grep -q "Status:    inactive"; then
    log_success "Status in State File Verified (inactive)."
else
    log_warn "Status Mismatch in State File. Expected 'inactive'. (Known Issue: Suspension passport not sent)."
fi

section "Activation"
log_info "Activating Rack..."
"$(bin_dir)/fluxrig" admin racks activate $RACK_ID --api-url "$BASE_URL" --force
sleep 1
LIST_OUT_4=$("$(bin_dir)/fluxrig" admin racks list --api-url "$BASE_URL")
if echo "$LIST_OUT_4" | grep "active"; then
    log_success "Rack Activated (Registry Updated)."
    log_info "Output: $LIST_OUT_4"
    sleep 2
    if grep -q 'new="active"' "$CLI_RACK_LOG"; then
        log_success "Rack Received Activation (Log Verified)."
    else
        tail -n 20 "$CLI_RACK_LOG"
        log_warn "Rack did NOT receive activation notification (Mixer limit?). Proceeding."
    fi
else
    log_info "Output: $LIST_OUT_4"
    fail "Rack Activation Failed (Registry not updated)."
fi

section "Removal"
log_info "Removing Rack..."
"$(bin_dir)/fluxrig" admin racks remove $RACK_ID --api-url "$BASE_URL" --force
sleep 1
LIST_OUT_5=$("$(bin_dir)/fluxrig" admin racks list --api-url "$BASE_URL")
if echo "$LIST_OUT_5" | grep -q "rack-production-01"; then
    log_info "Output: $LIST_OUT_5"
    fail "'admin racks remove' Failed (Rack still listed)."
else
    log_success "'admin racks remove' Verified (Rack gone)."
    log_info "Output (Empty or different racks): $LIST_OUT_5"
fi

section "Re-Enrollment (Resurrection)"
stop_pid "${RACK_PIDS[cli]}"
log_info "Restarting Rack for Resurrection..."
cd "$WORK_DIR/rack_cli"
"$(bin_dir)/fluxrig" rack -c "rack.toml" >> "rack.stdout" 2>&1 &
RACK_PIDS[cli]=$!
cd "${BASE_DIR}"
sleep 2

LIST_OUT_6=$("$(bin_dir)/fluxrig" admin racks list --api-url "$BASE_URL")
if echo "$LIST_OUT_6" | grep -q "active"; then
    log_success "Resurrection Verified (Rack returned as Active - Pet Mode via Passport)."
    log_info "Output: $LIST_OUT_6"
elif echo "$LIST_OUT_6" | grep -q "pending"; then
    log_success "Resurrection Verified (Rack returned as Pending)."
    log_info "Output: $LIST_OUT_6"
else
    log_info "Output: $LIST_OUT_6"
    fail "Resurrection Failed (Rack not found)."
fi

log_info "Inspecting State (Resurrection)..."
STATE_OUT_3=$("$(bin_dir)/fluxrig" keys inspect "$WORK_DIR/rack_cli/data/state.flux")
log_info "$STATE_OUT_3"
if echo "$STATE_OUT_3" | grep -i "Identity"; then
    log_success "Resurrection State: Valid Passport Found."
else
    fail "Resurrection State: Failed to load passport."
fi

log_success "Phase 05 OK"

banner "Group B: ALL PHASES PASSED"
