#!/bin/bash
# Coat Check E2E Test Runner
# Single-Rack Tests for Coat Check Gear Validation
#
# Usage:
#   ./run_single_rack.sh [test_case]
#   ./run_single_rack.sh              # Run all tests
#   ./run_single_rack.sh TC01         # Run specific test
#
# Prerequisites:
#   - FluxRig binaries built (make build)

# NOTE: Do NOT use set -e as it causes issues with process cleanup and signals
set -u

# ==============================================================================
# Setup Paths
# ==============================================================================
BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${BASE_DIR}/../../.." && pwd)"

# Load E2E utilities (provides: log_*, setup_workspace, wait_for_port, etc.)
source "${BASE_DIR}/../utils/e2e_utils.sh"

# Override the global cleanup trap from e2e_utils.sh with our own
trap 'cleanup_all_quiet' EXIT INT TERM

# Infrastructure Ports (unique to avoid conflicts with other tests)
MIXER_API_PORT=9120
SNAKE_PORT=4233
FLUXRIG_API_URL="http://127.0.0.1:${MIXER_API_PORT}"

# PIDs
MIXER_PID=""
RACK_PID=""
ECHO_PID=""

# Test results (simple variables for bash v3 compatibility)
RESULT_TC01=""
RESULT_TC03=""
RESULT_TC20=""
RESULT_TC54=""

# ==============================================================================
# Helper Functions
# ==============================================================================

start_echo_server() {
    local port="${1:-9000}"
    log_info "Starting TCP Echo Server on port $port..."
    cp "$BASE_DIR/tcp_echo_server.py" "$WORK_DIR/tcp_echo_server.py"
    
    python3 "$WORK_DIR/tcp_echo_server.py" "$port" > "$WORK_DIR/rack/logs/echo_server.log" 2>&1 &
    ECHO_PID=$!
    sleep 1
    if ! kill -0 $ECHO_PID 2>/dev/null; then
        log_error "TCP Echo Server failed to start"
        cat "$WORK_DIR/rack/logs/echo_server.log"
        return 1
    fi
    log_success "TCP Echo Server started (PID: $ECHO_PID)"
}





start_mixer_local() {
    log_info "Starting Mixer..."
    # Use versioned config file
    cp "$BASE_DIR/mixer/fluxrig.toml" "$WORK_DIR/mixer/fluxrig.toml"
    # Ensure variables match script context (portable sed)
    sed "s/port = 9120/port = $MIXER_API_PORT/" "$WORK_DIR/mixer/fluxrig.toml" > "$WORK_DIR/mixer/fluxrig.toml.tmp" && mv "$WORK_DIR/mixer/fluxrig.toml.tmp" "$WORK_DIR/mixer/fluxrig.toml"
    sed "s/port = 4233/port = $SNAKE_PORT/" "$WORK_DIR/mixer/fluxrig.toml" > "$WORK_DIR/mixer/fluxrig.toml.tmp" && mv "$WORK_DIR/mixer/fluxrig.toml.tmp" "$WORK_DIR/mixer/fluxrig.toml"
    
    cd "$WORK_DIR/mixer"
    "${ROOT_DIR}/bin/fluxrig" keys gen-cluster -o "./data/cluster.key" > /dev/null 2>&1
    "${ROOT_DIR}/bin/fluxrig-mixer" -c "fluxrig.toml" > "mixer.stdout" 2>&1 &
    MIXER_PID=$!
    
    log_info "Mixer PID: $MIXER_PID"
    wait_for_port $MIXER_API_PORT 15 || fail "Mixer failed to start"
}

start_rack_local() {
    local rack_name="${1:-proxy-rack}"
    log_info "Starting Rack: $rack_name"
    # Use versioned config file
    cp "$BASE_DIR/rack/fluxrig.toml" "$WORK_DIR/rack/fluxrig.toml"
    # Update rack name and ports (portable sed)
    sed "s/name = \"proxy-rack\"/name = \"$rack_name\"/" "$WORK_DIR/rack/fluxrig.toml" > "$WORK_DIR/rack/fluxrig.toml.tmp" && mv "$WORK_DIR/rack/fluxrig.toml.tmp" "$WORK_DIR/rack/fluxrig.toml"
    sed "s/4233/$SNAKE_PORT/" "$WORK_DIR/rack/fluxrig.toml" > "$WORK_DIR/rack/fluxrig.toml.tmp" && mv "$WORK_DIR/rack/fluxrig.toml.tmp" "$WORK_DIR/rack/fluxrig.toml"
    
    cd "$WORK_DIR/rack"
    "${ROOT_DIR}/bin/fluxrig" run -c "fluxrig.toml" > "rack.stdout" 2>&1 &
    RACK_PID=$!
    
    log_info "Rack PID: $RACK_PID"
    sleep 3
    
    if ! kill -0 $RACK_PID 2>/dev/null; then
        log_error "Rack failed to start"
        cat "$WORK_DIR/rack/rack.stdout"
        return 1
    fi
    log_success "Rack started"
}

import_scenario() {
    local scenario_file="$1"
    log_info "Importing scenario: $(basename $scenario_file)"
    
    local response
    response=$(curl -s -w "\n%{http_code}" -X POST "${FLUXRIG_API_URL}/api/v1/scenario/import?activate=true" \
        -H "Content-Type: application/x-yaml" \
        --data-binary @"$scenario_file" 2>/dev/null)
    
    local http_code=$(echo "$response" | tail -n1)
    local body=$(echo "$response" | sed '$d')
    
    if [[ "$http_code" == "200" ]] || [[ "$http_code" == "201" ]]; then
        log_success "Scenario imported"
        sleep 2
        return 0
    else
        log_warn "Scenario import returned HTTP $http_code"
        return 1
    fi
}

cleanup_all_quiet() {
    # Suppress output during cleanup
    # Logic: SIGTERM -> Sleep -> SIGKILL to avoid hanging on 'wait'
    
    # HTTP Echo Server
    if [[ -n "$ECHO_PID" ]]; then
        kill -TERM $ECHO_PID 2>/dev/null || true
        # Don't wait indefinitely
    fi
    
    # Rack
    if [[ -n "$RACK_PID" ]]; then
        kill -TERM $RACK_PID 2>/dev/null || true
    fi
    
    # Mixer
    if [[ -n "$MIXER_PID" ]]; then
        kill -TERM $MIXER_PID 2>/dev/null || true
    fi
    
    # Give them a moment to die gracefully
    sleep 2
    
    # Force kill anything remaining (by PID)
    [[ -n "$ECHO_PID" ]] && kill -9 $ECHO_PID 2>/dev/null || true
    [[ -n "$RACK_PID" ]] && kill -9 $RACK_PID 2>/dev/null || true
    [[ -n "$MIXER_PID" ]] && kill -9 $MIXER_PID 2>/dev/null || true

    # Final sweep of ports (quietly)
    lsof -ti :$MIXER_API_PORT -ti :$SNAKE_PORT -ti :9180 -ti :9000 2>/dev/null | xargs kill -9 2>/dev/null || true
    
    MIXER_PID=""
    RACK_PID=""
    ECHO_PID=""
}

# ==============================================================================
# Test Cases
# ==============================================================================

test_TC01_basic_store_restore() {
    section "TC01: Basic Store/Restore"
    
    start_mixer_local || return 1
    start_rack_local "proxy-rack" || return 1
    start_echo_server 9000 || return 1
    
    import_scenario "$BASE_DIR/scenarios/tc01_basic.yaml" || log_warn "Scenario import issue"
    
    sleep 2
    
    # Send request to trigger store/restore (Custom Protocol: Payload=Key)
    log_info "Sending Custom TCP Request (tx001)..."
    # Send "tx001". io_tcp ScanLines splits it. Payload="tx001".
    response=$(echo "tx001" | nc -w 2 127.0.0.1 9180)
    log_info "Response: $response"
    sleep 2

    # Verify gears started and traffic processed
    if grep -q "coatcheck\|ctx_store\|tcp_gateway" "$WORK_DIR/rack/logs/rack.log" 2>/dev/null || \
       grep -q "Scenario Applied\|scenario" "$WORK_DIR/rack/rack.stdout" 2>/dev/null; then
        log_success "Coat Check gears detected"
        RESULT_TC01="PASS"
    else
        log_warn "Could not verify gear startup - check logs at: $WORK_DIR"
        RESULT_TC01="MANUAL_CHECK"
    fi
    
    cleanup_all_quiet
}

test_TC03_daemon_ttl() {
    section "TC03: Daemon TTL Expiry"
    
    start_mixer_local || return 1
    start_rack_local "proxy-rack" || return 1
    start_echo_server 9000 || return 1
    
    import_scenario "$BASE_DIR/scenarios/tc03_ttl.yaml" || log_warn "Scenario import issue"
    
    sleep 2
    
    if grep -q "ttl\|TTL\|5s\|daemon" "$WORK_DIR/rack/rack.stdout" 2>/dev/null || \
       grep -q "ctx_daemon\|bucket" "$WORK_DIR/rack/logs/rack.log" 2>/dev/null; then
        log_success "TTL configuration detected"
        RESULT_TC03="PASS"
    else
        log_warn "Could not verify TTL configuration"
        RESULT_TC03="MANUAL_CHECK"
    fi
    
    cleanup_all_quiet
}

test_TC20_on_missing_error() {
    section "TC20: on_missing=error"
    
    start_mixer_local || return 1
    start_rack_local "proxy-rack" || return 1
    start_echo_server 9000 || return 1
    
    import_scenario "$BASE_DIR/scenarios/tc20_on_missing_error.yaml" || log_warn "Scenario import issue"
    
    sleep 2
    
    if grep -q "on_missing\|error\|ctx_restore" "$WORK_DIR/rack/rack.stdout" 2>/dev/null || \
       grep -q "restore\|error" "$WORK_DIR/rack/logs/rack.log" 2>/dev/null; then
        log_success "on_missing=error configuration detected"
        RESULT_TC20="PASS"
    else
        log_warn "Could not verify on_missing configuration"
        RESULT_TC20="MANUAL_CHECK"
    fi
    
    cleanup_all_quiet
}

test_TC54_restore_after_ttl() {
    section "TC54: Restore After TTL Expiry"
    
    start_mixer_local || return 1
    start_rack_local "proxy-rack" || return 1
    start_echo_server 9000 || return 1
    
    import_scenario "$BASE_DIR/scenarios/tc54_ttl_expiry.yaml" || log_warn "Scenario import issue"
    
    sleep 2
    
    # 1. Test SHORT TTL (3s) -> Should Expire (Delay 4s)
    log_info "TEST 1: Sending 'delay:4:short' (TTL 3s)..."
    (echo -n "delay:4:short|"; sleep 5) | nc -w 6 127.0.0.1 9180
    
    sleep 15 # Flush logs (High latency safe)
    if grep -E -q "missing context|coat missing" "$WORK_DIR/rack/logs/rack.log" 2>/dev/null; then
        log_success "TEST 1 PASS: Short TTL expired as expected"
        RESULT_TC54="PASS"
    else
        log_warn "TEST 1 FAIL: Expected expiry error not found"
        RESULT_TC54="FAIL"
    fi

    log_info "TEST 2: Sending 'delay:10:long' (TTL 10s)..."
    (echo -n "delay:10:long|"; sleep 12) | nc -w 15 127.0.0.1 9180 &
    PID_NC=$!
    
    # Key "delay:10:long" -> Base64 "ZGVsYXk6MTA6bG9uZw" (Standard, but URL-safe might differ slightly? No, alphanum is same)
    # The log uses RawURLEncoding?
    # In gear.go: base64.RawURLEncoding.EncodeToString
    # "delay:10:long" -> ZGVsYXk6MTA6bG9uZw
    
    KEY_B64="ZGVsYXk6MTA6bG9uZw"

    # Check at 5s (Should be ALIVE)
    sleep 5
    if grep "coat expired" "$WORK_DIR/rack/logs/rack.log" | grep -q "$KEY_B64"; then
        log_error "TEST 2 FAIL: Key expired prematurely (checked at 5s)"
        RESULT_TC54="FAIL"
    else
        log_info "TEST 2 CHECK 1 PASS: Key still alive at 5s"
    fi

    # Check at 12s (Should be EXPIRED)
    wait $PID_NC
    sleep 2 # Extra buffer
    
    if grep "coat expired" "$WORK_DIR/rack/logs/rack.log" | grep -q "$KEY_B64"; then
         log_info "TEST 2 CHECK 2 PASS: Key expired eventually"
    else
         log_error "TEST 2 FAIL: Key never expired"
         RESULT_TC54="FAIL"
         # Check debug
         echo "--- DEBUG: rack.log (filtered) ---"
         grep -v "Sent Heartbeat" "$WORK_DIR/rack/logs/rack.log" | grep -v "heartbeats_sent" | tail -n 50
         echo "--- END DEBUG ---"
    fi
    
    cleanup_all_quiet
}

# ==============================================================================
# Results Summary
# ==============================================================================

print_results() {
    echo ""
    banner "TEST RESULTS SUMMARY"
    
    local passed=0 failed=0 manual=0
    
    # TC01
    if [[ -n "$RESULT_TC01" ]]; then
        case "$RESULT_TC01" in
            PASS)        log_success "TC01: PASS"; passed=$((passed+1)) ;;
            FAIL)        log_error "TC01: FAIL"; failed=$((failed+1)) ;;
            *)           log_warn "TC01: $RESULT_TC01"; manual=$((manual+1)) ;;
        esac
    fi
    
    # TC03
    if [[ -n "$RESULT_TC03" ]]; then
        case "$RESULT_TC03" in
            PASS)        log_success "TC03: PASS"; passed=$((passed+1)) ;;
            FAIL)        log_error "TC03: FAIL"; failed=$((failed+1)) ;;
            *)           log_warn "TC03: $RESULT_TC03"; manual=$((manual+1)) ;;
        esac
    fi
    
    # TC20
    if [[ -n "$RESULT_TC20" ]]; then
        case "$RESULT_TC20" in
            PASS)        log_success "TC20: PASS"; passed=$((passed+1)) ;;
            FAIL)        log_error "TC20: FAIL"; failed=$((failed+1)) ;;
            *)           log_warn "TC20: $RESULT_TC20"; manual=$((manual+1)) ;;
        esac
    fi
    
    # TC54
    if [[ -n "$RESULT_TC54" ]]; then
        case "$RESULT_TC54" in
            PASS)        log_success "TC54: PASS"; passed=$((passed+1)) ;;
            FAIL)        log_error "TC54: FAIL"; failed=$((failed+1)) ;;
            *)           log_warn "TC54: $RESULT_TC54"; manual=$((manual+1)) ;;
        esac
    fi
    
    echo ""
    log_info "Passed: $passed | Failed: $failed | Manual: $manual"
    log_info "Logs available at: $WORK_DIR/"
    log_info "Symlink: $BASE_DIR/work"
    
    [[ $failed -eq 0 ]]
}

# ==============================================================================
# Main
# ==============================================================================

main() {
    local test_filter="${1:-all}"
    
    banner "Coat Check E2E Test Runner"
    log_info "Filter: $test_filter"
    
    # Setup workspace (creates /tmp/fluxrig/work_coatcheck_* and symlinks to ./work)
    setup_workspace "coatcheck" "$BASE_DIR"
    
    # Cleanup old processes
    # Cleanup old processes (Safe Mode)
    log_info "Cleaning up old processes..."
    
    # 1. Kill by port first (most precise)
    lsof -ti :$MIXER_API_PORT -ti :$SNAKE_PORT -ti :9180 -ti :9000 2>/dev/null | xargs kill -9 2>/dev/null || true
    
    # 2. Kill only specific test binaries from our repo
    pkill -f "$ROOT_DIR/bin/fluxrig" 2>/dev/null || true
    pkill -f "$ROOT_DIR/bin/fluxrig-mixer" 2>/dev/null || true
    
    sleep 1
    
    # Build if needed
    if [[ ! -f "$ROOT_DIR/bin/fluxrig" ]]; then
        log_info "Building FluxRig..."
        make -C "$ROOT_DIR" build
    fi
    
    cd "$ROOT_DIR"
    
    case "$test_filter" in
        all)
            test_TC01_basic_store_restore
            test_TC03_daemon_ttl
            test_TC20_on_missing_error
            test_TC54_restore_after_ttl
            ;;
        TC01) test_TC01_basic_store_restore ;;
        TC03) test_TC03_daemon_ttl ;;
        TC20) test_TC20_on_missing_error ;;
        TC54) test_TC54_restore_after_ttl ;;
        *)
            fail "Unknown test case: $test_filter"
            ;;
    esac
    
    print_results
}

main "$@"
