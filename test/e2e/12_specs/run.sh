#!/bin/bash

# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
# Spec & Scenario Manager E2E Test Runner
# Validates full integration: CLI (Spec Manager) + API (Scenario Controller)
#
# Usage:
#   ./run.sh [test_case]
#   ./run.sh              # Run all tests
#

set -u

# ==============================================================================
# Setup Paths (Adhering to e2e_guide.md)
# ==============================================================================
BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"
SUITE_NAME="specs"

# We use utils from parent dir
source "${BASE_DIR}/../utils/e2e_utils.sh"

# ==============================================================================
# Global Variables
# ==============================================================================
MIXER_PID=""
API_URL="http://127.0.0.1:8090/api/v1"
STORE_DIR=""
SCENARIO_DIR="${BASE_DIR}/scenarios"

# ==============================================================================
# Helper Functions
# ==============================================================================

# Wrapper for CLI (Spec Manager)
run_fluxrig_spec() {
    "${FLUXRIG_BIN}" spec "$@" --store-dir "${STORE_DIR}"
}

# Wrapper for CLI (Scenario Manager via API)
run_fluxrig_scenario() {
    # fluxrig scenario import uses hardcoded URL or flag, let's assumes flag exists or default
    # If no flag, we use curl as fallback for raw API test
    "${FLUXRIG_BIN}" scenario "$@"
}

cleanup_mixer() {
    if [[ -n "$MIXER_PID" ]] && kill -0 "$MIXER_PID" 2>/dev/null; then
        kill "$MIXER_PID" 2>/dev/null || true
        # Wait up to 2s for graceful shutdown, then force-kill
        for i in 1 2; do
            kill -0 "$MIXER_PID" 2>/dev/null || break
            sleep 1
        done
        kill -9 "$MIXER_PID" 2>/dev/null || true
    fi
    # Chain base cleanup (kills any remaining background jobs)
    cleanup
}

trap cleanup_mixer EXIT

prepare_environment() {
    setup_workspace "specs" "${BASE_DIR}"

    FLUXRIG_BIN="${WORK_DIR}/fluxrig"
    OUTPUT="${WORK_DIR}/output.log"
    
    log_info "Building fluxrig..."
    make -C "$ROOT_DIR" build-bin > /dev/null
    cp "$ROOT_DIR/bin/fluxrig" "${FLUXRIG_BIN}"
    cp "$ROOT_DIR/bin/fluxrig-mixer" "${WORK_DIR}/fluxrig-mixer"

    # Shared Data Directory for CLI and Mixer
    # This simulates "Production" where CLI operates on the Server's data dir
    mkdir -p "${WORK_DIR}/mixer"
    STORE_DIR="${WORK_DIR}/mixer/data"
    mkdir -p "${STORE_DIR}"
}

start_mixer() {
    log_info "Starting Mixer (Port 8090)..."
    cd "${WORK_DIR}/mixer"
    
    # Create minimal config
    cat <<EOF > fluxrig-mixer.toml
[mixer]
machine_id = 1
mixer_name = "mixer-specs-e2e"

[api]
port = 8090

[store]
dir = "./data"
database_file = "flux.duckdb"

[logging]
level = "debug"

[enrollment]
auto_adopt = true
EOF

    "${FLUXRIG_BIN}" keys gen-cluster -o "./data/cluster.key" > /dev/null

    "${WORK_DIR}/fluxrig-mixer" -c fluxrig-mixer.toml > mixer.stdout 2>&1 &
    MIXER_PID=$!
    cd "${BASE_DIR}"

    wait_for_port 8090 15 || fail "Mixer failed to start"
    log_success "Mixer Started (PID: $MIXER_PID)"
}

# ==============================================================================
# CLI Tests (Spec Manager)
# ==============================================================================

test_cli_spec_lifecycle() {
    section "CLI Spec Lifecycle (Local Store)"
    
    # 1. Import with Explicit Tag
    log_info "Importing visa:v1.0.0..."
    run_fluxrig_spec import "${SCENARIO_DIR}/visa.yaml" --name visa --tag v1.0.0 > "${OUTPUT}" 2>&1
    if grep -q "Imported visa:v1.0.0" "${OUTPUT}"; then
        log_success "Imported visa:v1.0.0"
    else
        log_error "Failed to import visa:v1.0.0"
        cat "${OUTPUT}"
        return 1
    fi

    # 2. Idempotency
    if run_fluxrig_spec import "${SCENARIO_DIR}/visa.yaml" --name visa --tag v1.0.0 > /dev/null 2>&1; then
        log_success "Idempotency Verified"
    else
        log_error "Idempotency check failed"
        return 1
    fi

    # 3. Invalid Version (Non-Happy Path)
    if run_fluxrig_spec import "${SCENARIO_DIR}/visa.yaml" --name visa --tag "invalid-tag" > "${OUTPUT}" 2>&1; then
        log_error "Should have failed (invalid version)"
        return 1
    else
        log_success "Rejected invalid version (as expected)"
    fi

    # 4. Conflict Detection (Immutable)
    if run_fluxrig_spec import "${SCENARIO_DIR}/visa_mod.yaml" --name visa --tag v1.0.0 > "${OUTPUT}" 2>&1; then
        log_error "Should have failed (conflict)"
        return 1
    else
        if grep -q "already exists" "${OUTPUT}"; then
            log_success "Conflict detected correctly"
        else
            log_warn "Failed but message differed: $(cat ${OUTPUT})"
        fi
    fi

    # 5. List Specs
    run_fluxrig_spec list > "${OUTPUT}" 2>&1
    if grep -q "visa.*v1.0.0" "${OUTPUT}"; then
        log_success "List verified"
    else
        log_error "List missing visa:v1.0.0"
        return 1
    fi
}

# ==============================================================================
# API Tests (Scenario Integration)
# ==============================================================================

test_api_scenario_lifecycle() {
    section "API Scenario Lifecycle (Mixer Integration)"

    # 1. Import Scenario via API (using curl for raw control)
    log_info "Importing Scenario via API..."
    
    HTTP_CODE=$(curl -s -o "${OUTPUT}" -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
        -H "Content-Type: application/x-yaml" \
        --data-binary @"${SCENARIO_DIR}/scenario_v1.yaml")

    if [[ "$HTTP_CODE" == "200" ]] || [[ "$HTTP_CODE" == "201" ]]; then
        log_success "Scenario Imported (HTTP $HTTP_CODE)"
    else
        log_error "Scenario Import Failed (HTTP $HTTP_CODE)"
        cat "${OUTPUT}"
        return 1
    fi

    # 2. Verify Topology Status
    log_info "Verifying Topology Status..."
    STATUS_JSON=$(curl -s "${API_URL}/topology/status")
    
    # We expect ActiveVer to be non-empty/non-unknown
    if [[ "$STATUS_JSON" =~ "active_ver" ]] && [[ "$STATUS_JSON" != *"unknown"* ]]; then
         log_success "Topology Status Verified: $STATUS_JSON"
    else
         log_error "Topology Status Invalid: $STATUS_JSON"
         return 1
    fi

    # 3. Persistence Check (Check disk)
    if [[ -f "${STORE_DIR}/scenarios/active" ]]; then
         log_success "Scenario persisted to disk"
    else
         log_error "Scenario file missing in ${STORE_DIR}/scenarios"
         ls -R "${STORE_DIR}"
         return 1
    fi
}

test_concurrent_access() {
    section "Concurrent Access (CLI + Mixer)"
    
    # While Mixer is running, use CLI to import another spec
    log_info "Importing mastercard:v2.0.0 via CLI while Mixer runs..."
    
    run_fluxrig_spec import "${SCENARIO_DIR}/mc.yaml" --name mastercard --tag v2.0.0 > "${OUTPUT}" 2>&1
    if grep -q "Imported mastercard:v2.0.0" "${OUTPUT}"; then
        log_success "CLI import succeeded during Mixer runtime"
    else
        log_error "CLI import failed"
        cat "${OUTPUT}"
        return 1
    fi
    
    # Validate Mixer didn't crash
    if ! kill -0 $MIXER_PID 2>/dev/null; then
        log_error "Mixer crashed during CLI operation!"
        return 1
    fi
    log_success "Mixer stability confirmed"
}

# ==============================================================================
# Main
# ==============================================================================

prepare_environment

# 1. Run CLI Tests (Pre-Mixer)
test_cli_spec_lifecycle || fail "CLI tests failed"

# 2. Start Mixer (Simulate Server Start)
start_mixer

# 3. Run API Tests
test_api_scenario_lifecycle || fail "API tests failed"
test_concurrent_access || fail "Concurrent tests failed"

banner "Specs E2E PASSED"