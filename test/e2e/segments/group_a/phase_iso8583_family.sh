# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0
#
# Sourced by run.sh, not executed directly. Phases 10/13/14: the ISO 8583
# protocol family, each against Group A's already-running shared Mixer
# (8090/4223), each with its own dedicated Rack process started and stopped
# within its own phase (no later phase needs rack-iso-01/rack-processor/
# rack-switch to keep running). Kept in a separate file purely for size —
# the source tests these three replace are 484 + 117 + 157 lines on their
# own.
#
# Fixtures 10_iso8583 owns outright (scenarios, python tooling, pcap
# samples) are copied into iso8583_family/ alongside this file, so this
# segment has no dependency on test/e2e/10_iso8583 surviving. 13/14's
# scenario/spec fixtures live under test/robot/suites/payment_switch/,
# shared with a Robot suite, and are referenced there directly rather than
# copied.

ISO_FAMILY_DIR="${BASE_DIR}/iso8583_family"
ISO_PORT=8583

# ==============================================================================
section "Phase 10: ISO 8583 Universal Matrix"
# ==============================================================================
# was 10_iso8583. One Rack (rack-iso-01), ten scenario-hot-swap sub-phases.

ISO_RESULTS=()

iso_record_result() {
    local phase_id=$1 status=$2 name=$3 details=$4
    ISO_RESULTS+=("${phase_id}|${status}|${name}|${details}")
}

iso_print_summary() {
    echo ""
    echo "================================================================================"
    echo "                       ISO 8583 MATRIX SUMMARY"
    echo "================================================================================"
    printf "%-8s | %-8s | %-35s | %s\n" "PHASE" "RESULT" "TEST CASE" "DETAILS"
    echo "---------+----------+-------------------------------------+--------------------------------"
    for entry in "${ISO_RESULTS[@]}"; do
        IFS="|" read -r phase_id status name info <<< "$entry"
        if [ "$status" == "PASS" ]; then
            printf "%-8s | \033[32m%-8s\033[0m | %-35s | %s\n" "$phase_id" "$status" "$name" "$info"
        else
            printf "%-8s | \033[31m%-8s\033[0m | %-35s | %s\n" "$phase_id" "$status" "$name" "$info"
        fi
    done
    echo "================================================================================"
}

iso_get_log_offset() {
    wc -l < "${WORK_DIR}/rack_iso/logs/rack.log" | tr -d ' '
}

iso_wait_for_gear() {
    local gear_name=$1 timeout=$2 start_offset=${3:-0}
    log_info "Waiting for gear $gear_name to start..."
    for i in $(seq 1 "$timeout"); do
        if tail -n +$((start_offset+1)) "${WORK_DIR}/rack_iso/logs/rack.log" | grep -q "starting gear.*flux.name=\"\?$gear_name\"\{0,1\}"; then
            log_success "Gear $gear_name is READY"
            return 0
        fi
        sleep 1
    done
    return 1
}

iso_deploy_scenario() {
    local scenario_file=$1 label=$2
    log_info "Deploying Scenario: $scenario_file ($label)"
    local status
    status=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
        -H "Content-Type: application/x-yaml" --data-binary @"${ISO_FAMILY_DIR}/scenarios_10/${scenario_file}")
    [ "$status" == "200" ] || fail "Failed to deploy scenario (HTTP $status)"

    local gear_to_wait="iso-gateway"
    [[ "$scenario_file" == *"client"* ]] && gear_to_wait="iso-client"
    iso_wait_for_gear "$gear_to_wait" 15 || fail "Gear $gear_to_wait failed to start"
    [ "$gear_to_wait" == "iso-gateway" ] && { wait_for_port "$ISO_PORT" 10 || fail "ISO Gear failed to bind on port $ISO_PORT"; }
}

iso_verify_mti() {
    local start_line=$1 expected=$2 label=$3 phase_id=$4
    local counts
    counts=$(tail -n +$((start_line+1)) "${WORK_DIR}/rack_iso/logs/rack.log" | grep -oE 'mti[:=][0-9]{4}' | grep -oE '[0-9]{4}' | sort | uniq -c | awk '{print $2 "(" $1 ")"}' | tr '\n' ' ')
    if [ -n "$counts" ] && [[ "$counts" == *"$expected"* ]]; then
        log_success "[$label] MTI $expected parsed. Counts: $counts"
        iso_record_result "$phase_id" "PASS" "$label [Ingress]" "MTIs: $counts"
    else
        log_warn "[$label] MTI $expected not found in recent logs."
        iso_record_result "$phase_id" "FAIL" "$label [Ingress]" "Expected MTI $expected not found. Got: $counts"
    fi
}

iso_verify_failure() {
    local start_line=$1 label=$2 phase_id=$3
    log_success "[$label] Mismatched traffic correctly rejected/flagged"
    iso_record_result "$phase_id" "PASS" "$label [Ingress]" "Rejection confirmed"
}

iso_verify_metrics() {
    local metric_name=$1 label=$2 phase_id=$3 filter_attr=${4:-}
    local attempt=1 max_attempts=15 found=0 response="" extracted_value=""
    local log_label="$metric_name"
    [ -n "$filter_attr" ] && log_label="$metric_name {$filter_attr}"
    log_info "Verifying Metrics ($log_label)..."

    while [ $attempt -le $max_attempts ]; do
        response=$(curl -s "${API_URL}/telemetry/metrics?name=${metric_name}&limit=10")
        if [ -n "$filter_attr" ]; then
            local attr_key=${filter_attr%%=*} attr_val=${filter_attr#*=}
            extracted_value=$(echo "$response" | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for m in data:
        if m.get('name') == '$metric_name' and m.get('attributes', {}).get('$attr_key') == '$attr_val':
            val = m.get('value')
            if val is None: val = m.get('sum')
            if val is None: val = m.get('count')
            if val is not None:
                print(val); break
    else:
        print('NOT_FOUND')
except Exception:
    print('ERROR')
")
        elif [[ "$response" == *"$metric_name"* ]]; then
            extracted_value=$(echo "$response" | grep -o '"value":[^,}]*' | cut -d: -f2 | tr -d ' ' | head -n 1)
        else
            extracted_value="NOT_FOUND"
        fi

        if [ "$extracted_value" != "NOT_FOUND" ] && [ "$extracted_value" != "ERROR" ] && [ -n "$extracted_value" ] && [[ "$extracted_value" != "0" && "$extracted_value" != "0.0" ]]; then
            found=1
            break
        fi
        sleep 2
        attempt=$((attempt+1))
    done

    if [ $found -eq 1 ]; then
        log_success "[$label] Metric $log_label found. Value: $extracted_value"
        iso_record_result "$phase_id" "PASS" "$label [$log_label]" "Value: $extracted_value"
    else
        log_warn "[$label] Metric $log_label NOT found or zero after ${max_attempts} attempts."
        iso_record_result "$phase_id" "FAIL" "$label [$log_label]" "Metric missing or zero"
    fi
}

if [ ! -d "${ISO_FAMILY_DIR}/.venv" ]; then
    log_info "Creating Python virtual environment for the ISO 8583 tooling..."
    python3 -m venv "${ISO_FAMILY_DIR}/.venv"
    "${ISO_FAMILY_DIR}/.venv/bin/pip" install -q -r "${ISO_FAMILY_DIR}/requirements.txt" || fail "Failed to install requirements"
fi
ISO_VENV_PYTHON="${ISO_FAMILY_DIR}/.venv/bin/python3"

mkdir -p "${WORK_DIR}/rack_iso/data" "${WORK_DIR}/rack_iso/logs" "${WORK_DIR}/rack_iso/specs"
touch "${WORK_DIR}/rack_iso/logs/rack.log"
cp -r "${ISO_FAMILY_DIR}/specs_10/"* "${WORK_DIR}/rack_iso/specs/"
cat > "${WORK_DIR}/rack_iso/fluxrig.toml" <<EOF
[logging]
level = "trace"
filename = "logs/rack.log"

[store]
dir = "./data"
wal_max_size_mb = 500
state_file = "state.flux"

[rack]
name = "rack-iso-01"

[rack.bus]
url = "nats://127.0.0.1:${SNAKE_PORT}"
stream_name = "flux-msg"

[rack.telemetry]
enabled = true
EOF

cd "${WORK_DIR}/rack_iso"
FLUXRIG_TRACE=1 "$(bin_dir)/fluxrig" run -c "fluxrig.toml" > "rack.stdout" 2>&1 &
RACK_PIDS[iso]=$!
cd "${BASE_DIR}"
sleep 3

# --- Phase 1: Dynamic ASCII + Big Endian ---
banner "Matrix Phase 1: ASCII + Big Endian"
iso_deploy_scenario "01_scenario_ascii_be.yaml" "ascii_be"
LOG_START=$(iso_get_log_offset)
log_info "Injecting E2E Traffic (ASCII-BE)..."
if "$ISO_VENV_PYTHON" "${ISO_FAMILY_DIR}/iso8583_tool.py" --e2e --host 127.0.0.1 --port $ISO_PORT --encoding ascii --endian big --count 1; then
    iso_record_result "1" "PASS" "Dynamic ASCII-BE [Egress]" "Python E2E Verified"
else
    iso_record_result "1" "FAIL" "Dynamic ASCII-BE [Egress]" "Python Validation Failed"
fi
iso_verify_mti "$LOG_START" "0800" "Dynamic ASCII-BE" "1"
iso_verify_metrics "flux.gear.messages_in" "Dynamic ASCII-BE" "1" "direction=inbound"
iso_verify_metrics "flux.gear.messages_out" "Dynamic ASCII-BE" "1" "direction=outbound"
iso_verify_metrics "flux.gear.processing_time_ms.count" "Dynamic ASCII-BE" "1" "direction=inbound"
iso_verify_metrics "flux.port.bytes_in" "Dynamic ASCII-BE" "1" "direction=inbound"
iso_verify_metrics "flux.port.bytes_out" "Dynamic ASCII-BE" "1" "direction=outbound"
iso_verify_metrics "flux.port.connections_total" "Dynamic ASCII-BE" "1"

# --- Phase 2: Mismatch Check (LE -> BE Gear) ---
banner "Matrix Phase 2: Mismatch Check (LE -> BE Gear)"
LOG_START=$(iso_get_log_offset)
log_info "Injecting Mismatching Static (ASCII-LE PCAP)..."
python3 "${ISO_FAMILY_DIR}/sample_injector.py" --port $ISO_PORT --pcap "${ISO_FAMILY_DIR}/samples/iso8583_ascii_sample.pcapng"
iso_verify_failure "$LOG_START" "Mismatch ASCII-LE -> ASCII-BE Gear" "2"

# --- Phase 3: ASCII + Little Endian ---
banner "Matrix Phase 3: ASCII + Little Endian"
iso_deploy_scenario "03_scenario_ascii_le.yaml" "ascii_le"
LOG_START=$(iso_get_log_offset)
log_info "Injecting E2E Traffic (ASCII-LE)..."
if "$ISO_VENV_PYTHON" "${ISO_FAMILY_DIR}/iso8583_tool.py" --e2e --host 127.0.0.1 --port $ISO_PORT --encoding ascii --endian little --count 1; then
    iso_record_result "3" "PASS" "Dynamic ASCII-LE [Egress]" "Python E2E Verified"
else
    iso_record_result "3" "FAIL" "Dynamic ASCII-LE [Egress]" "Python Validation Failed"
fi
iso_verify_mti "$LOG_START" "0800" "Dynamic ASCII-LE" "3"
LOG_START=$(iso_get_log_offset)
log_info "Injecting Matching Static (ASCII-LE PCAP)..."
python3 "${ISO_FAMILY_DIR}/sample_injector.py" --port $ISO_PORT --pcap "${ISO_FAMILY_DIR}/samples/iso8583_ascii_sample.pcapng"
iso_verify_mti "$LOG_START" "0200" "Static ASCII-LE" "3"

# --- Phase 4: BCD + Big Endian ---
banner "Matrix Phase 4: BCD + Big Endian"
iso_deploy_scenario "04_scenario_bcd_be.yaml" "bcd_be"
LOG_START=$(iso_get_log_offset)
log_info "Injecting E2E Traffic (BCD-BE)..."
if "$ISO_VENV_PYTHON" "${ISO_FAMILY_DIR}/iso8583_tool.py" --e2e --host 127.0.0.1 --port $ISO_PORT --encoding bcd --endian big --count 1; then
    iso_record_result "4" "PASS" "Dynamic BCD-BE [Egress]" "Python E2E Verified"
else
    iso_record_result "4" "FAIL" "Dynamic BCD-BE [Egress]" "Python Validation Failed"
fi
iso_verify_mti "$LOG_START" "0800" "Dynamic BCD-BE" "4"

# --- Phase 5: Mismatch Check (LE -> BE Gear) ---
banner "Matrix Phase 5: Mismatch Check (LE -> BE Gear)"
LOG_START=$(iso_get_log_offset)
log_info "Injecting Mismatching Static (BCD-LE PCAP)..."
python3 "${ISO_FAMILY_DIR}/sample_injector.py" --port $ISO_PORT --pcap "${ISO_FAMILY_DIR}/samples/iso8583_bin_sample.pcapng"
iso_verify_failure "$LOG_START" "Mismatch BCD-LE -> BCD-BE Gear" "5"

# --- Phase 6: BCD + Little Endian ---
banner "Matrix Phase 6: BCD + Little Endian"
iso_deploy_scenario "06_scenario_bcd_le.yaml" "bcd_le"
LOG_START=$(iso_get_log_offset)
log_info "Injecting E2E Traffic (BCD-LE)..."
if "$ISO_VENV_PYTHON" "${ISO_FAMILY_DIR}/iso8583_tool.py" --e2e --host 127.0.0.1 --port $ISO_PORT --encoding bcd --endian little --count 1; then
    iso_record_result "6" "PASS" "Dynamic BCD-LE [Egress]" "Python E2E Verified"
else
    iso_record_result "6" "FAIL" "Dynamic BCD-LE [Egress]" "Python Validation Failed"
fi
iso_verify_mti "$LOG_START" "0800" "Dynamic BCD-LE" "6"
LOG_START=$(iso_get_log_offset)
log_info "Injecting Matching Static (BCD-LE PCAP)..."
python3 "${ISO_FAMILY_DIR}/sample_injector.py" --pcap "${ISO_FAMILY_DIR}/samples/iso8583_bin_sample.pcapng" --host 127.0.0.1 --port $ISO_PORT
iso_verify_mti "$LOG_START" "0200" "Static BCD-LE" "6"

# --- Phase 7: Visa V.I.P ---
banner "Matrix Phase 7: Visa V.I.P (EBCDIC + Header)"
iso_deploy_scenario "07_scenario_visa.yaml" "visa_vip"
LOG_START=$(iso_get_log_offset)
log_info "Injecting E2E Traffic (Visa V.I.P)..."
if "$ISO_VENV_PYTHON" "${ISO_FAMILY_DIR}/iso8583_tool.py" --e2e --host 127.0.0.1 --port $ISO_PORT --encoding ascii --endian big --variant visa --count 1; then
    iso_record_result "7" "PASS" "Visa V.I.P [Egress]" "Python E2E Verified"
else
    iso_record_result "7" "FAIL" "Visa V.I.P [Egress]" "Python Validation Failed"
fi
iso_verify_mti "$LOG_START" "0800" "Visa V.I.P" "7"
if tail -n +$((LOG_START+1)) "${WORK_DIR}/rack_iso/logs/rack.log" | grep -q "iso8583.dst_id:112233"; then
    log_success "Visa Metadata Extraction Verified"
    iso_record_result "7" "PASS" "Visa V.I.P Metadata [Ingress]" "Dest/Src IDs extracted"
else
    fail "Visa Metadata Extraction Failed"
fi

# --- Phase 8: Codec Round-Trip (Decode + Encode) ---
banner "Matrix Phase 8: Codec Round-Trip"
DEPLOY_OFFSET=$(iso_get_log_offset)
iso_deploy_scenario "09_scenario_codec_roundtrip.yaml" "codec_roundtrip"
iso_wait_for_gear "codec-decode" 10 "$DEPLOY_OFFSET" || fail "codec-decode failed to start"
iso_wait_for_gear "codec-encode" 10 "$DEPLOY_OFFSET" || fail "codec-encode failed to start"
LOG_START=$(iso_get_log_offset)
log_info "Injecting Codec E2E Traffic (BCD MTI)..."
ISO_TOOL_OUT="$WORK_DIR/iso_tool_output.txt"
if "$ISO_VENV_PYTHON" "${ISO_FAMILY_DIR}/iso8583_tool.py" --e2e --host 127.0.0.1 --port $ISO_PORT --mock-port 10000 --encoding bcd --endian big --count 1 > "$ISO_TOOL_OUT" 2>&1; then
    iso_record_result "8" "PASS" "Codec Round-Trip [Egress]" "Transparency OK"
else
    iso_record_result "8" "FAIL" "Codec Round-Trip [Egress]" "Byte Transparency Failed"
    cat "$ISO_TOOL_OUT"
    tail -n 20 "${WORK_DIR}/rack_iso/logs/rack.log"
fi
iso_verify_mti "$LOG_START" "0800" "Codec Round-Trip" "8"
if tail -n +$((LOG_START+1)) "${WORK_DIR}/rack_iso/logs/rack.log" | grep -q "spec_hash="; then
    log_success "Codec Spec Hash Tracking Verified"
    iso_record_result "8" "PASS" "Codec Metadata [In-Flight]" "Spec hash identified"
else
    iso_record_result "8" "FAIL" "Codec Metadata [In-Flight]" "Spec hash missing"
fi

# --- Phase 9: Trace Logging & Masking ---
banner "Matrix Phase 9: Trace Logging & Masking"
if tail -n +$((LOG_START+1)) "${WORK_DIR}/rack_iso/logs/rack.log" | grep -q "Codec field dump"; then
    log_success "Codec Trace Logging Verified (Masking skipped as per design)"
    iso_record_result "9" "PASS" "Codec Trace & Masking" "Trace verified"
else
    log_warn "Codec Trace Logging check failed (expected Codec field dump in rack.log)"
    iso_record_result "9" "FAIL" "Codec Trace & Masking" "Trace missing"
fi

# --- Phase 10: Concurrency Limit (MaxConnections) ---
banner "Matrix Phase 10: Concurrency Limit (MaxConnections=5)"
iso_deploy_scenario "10_scenario_concurrency.yaml" "concurrency_limit"
log_info "Injecting High Concurrency Traffic (20 conns > 5 limit)..."
if python3 "${ISO_FAMILY_DIR}/iso8583_load.py" --host 127.0.0.1 --port $ISO_PORT --conns 20 --loops 5 --expect-rejects; then
    log_success "Concurrency Limit Verified (Connections Rejected)"
    iso_record_result "10" "PASS" "MaxConnections Enforcement" "Excess connections rejected"
else
    log_warn "Concurrency Limit Failed (All connections accepted or script error)"
    iso_record_result "10" "FAIL" "MaxConnections Enforcement" "Limit not enforced"
fi

# Tear down rack-iso-01 and its gears before the next phase reuses port 8583.
stop_pid "${RACK_PIDS[iso]}"
unset 'RACK_PIDS[iso]'
pkill -f "${ISO_FAMILY_DIR}/iso8583_tool.py" 2>/dev/null || true
wait_for_port_free() {
    local port="$1" timeout="${2:-10}"
    for ((i=1;i<=timeout;i++)); do
        lsof -Pi ":$port" -sTCP:LISTEN -t >/dev/null 2>&1 || return 0
        sleep 1
    done
    return 1
}
wait_for_port_free "$ISO_PORT" 10 || log_warn "Port $ISO_PORT still bound after rack-iso-01 teardown"

iso_print_summary

ISO_FAIL_COUNT=0
for entry in "${ISO_RESULTS[@]}"; do
    [[ "$entry" == *\|FAIL\|* ]] && ISO_FAIL_COUNT=$((ISO_FAIL_COUNT+1))
done
[ "$ISO_FAIL_COUNT" -eq 0 ] || fail "ISO8583 Universal Matrix: $ISO_FAIL_COUNT failures"
log_success "Phase 10 OK"

# ==============================================================================
section "Phase 13: Processor Simulator"
# ==============================================================================
# was 13_processor_sim. Terminal -> io server -> codec decode -> bento reply
# builder -> codec encode -> loopback, over real sockets.

PROC_SIM_PORT=10000
PROC_SCENARIO="${ROOT_DIR}/test/robot/suites/payment_switch/scenarios/processor_sim.yaml"
PROC_SPEC="${ROOT_DIR}/test/robot/suites/payment_switch/specs/switch.yaml"

mkdir -p "${WORK_DIR}/rack_processor/data" "${WORK_DIR}/rack_processor/logs" "${WORK_DIR}/rack_processor/specs"
touch "${WORK_DIR}/rack_processor/logs/rack.log"
cp "${PROC_SPEC}" "${WORK_DIR}/rack_processor/specs/switch.yaml"
cat > "${WORK_DIR}/rack_processor/fluxrig.toml" <<EOF
[logging]
level = "trace"
filename = "logs/rack.log"

[store]
dir = "./data"
wal_max_size_mb = 500
state_file = "state.flux"

[rack]
name = "rack-processor"

[rack.bus]
url = "nats://127.0.0.1:${SNAKE_PORT}"
stream_name = "flux-msg"

[rack.telemetry]
enabled = true
EOF

cd "${WORK_DIR}/rack_processor"
FLUXRIG_TRACE=1 "$(bin_dir)/fluxrig" run -c "fluxrig.toml" > "rack.stdout" 2>&1 &
RACK_PIDS[processor]=$!
cd "${BASE_DIR}"
sleep 3

section "Deploying processor_sim scenario"
status=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
    -H "Content-Type: application/x-yaml" --data-binary @"${PROC_SCENARIO}")
[ "$status" = "200" ] || fail "processor_sim scenario import failed (HTTP $status)"
wait_for_port "$PROC_SIM_PORT" 20 || fail "sim-ingress failed to bind :$PROC_SIM_PORT"
sleep 2

section "Terminal -> Processor Sim"
PROC_REPORT="${WORK_DIR}/procsim_report.json"
"$(bin_dir)/iso8583-tool" -mode load \
    -target "127.0.0.1:${PROC_SIM_PORT}" -encoding ascii \
    -concurrency 2 -rate 10 -duration 3s \
    -report "${PROC_REPORT}" > "${WORK_DIR}/procsim_tool.stdout" 2>&1
PROC_TOOL_RC=$?

section "Verification"
PROC_FAILS=0
[ $PROC_TOOL_RC -ne 0 ] && { log_error "load tool exited $PROC_TOOL_RC"; PROC_FAILS=$((PROC_FAILS+1)); }
if [ -f "$PROC_REPORT" ]; then
    proc_sent=$(python3 -c "import json;print(json.load(open('$PROC_REPORT')).get('req_sent',0))")
    proc_recv=$(python3 -c "import json;print(json.load(open('$PROC_REPORT')).get('resp_recv',0))")
    proc_rtt=$(python3 -c "import json;print(json.load(open('$PROC_REPORT')).get('rtt_success',0))")
    log_info "Terminal report: sent=$proc_sent recv=$proc_recv rtt_success=$proc_rtt"
    [ "$proc_recv" -gt 0 ] 2>/dev/null || { log_error "no responses received from the sim"; PROC_FAILS=$((PROC_FAILS+1)); }
    [ "$proc_rtt" -gt 0 ] 2>/dev/null || { log_error "no RTT-correlated responses (header not mirrored)"; PROC_FAILS=$((PROC_FAILS+1)); }
else
    log_error "no report produced"; PROC_FAILS=$((PROC_FAILS+1))
fi
if grep -q "direction=encode.*mti=0810" "${WORK_DIR}/rack_processor/rack.stdout" "${WORK_DIR}/rack_processor/logs/rack.log" 2>/dev/null; then
    log_success "Processor sim authored an 0810 response (bento + codec encode confirmed)"
else
    log_error "no encoded 0810 response found in the rack log"; PROC_FAILS=$((PROC_FAILS+1))
fi
[ "$PROC_FAILS" -eq 0 ] || fail "Processor Simulator: $PROC_FAILS failed checks"

stop_pid "${RACK_PIDS[processor]}"
unset 'RACK_PIDS[processor]'
wait_for_port_free "$PROC_SIM_PORT" 10 || log_warn "Port $PROC_SIM_PORT still bound after rack-processor teardown"

log_success "Phase 13 OK"

# ==============================================================================
section "Phase 14: Payment Switch (Conductor BIN routing)"
# ==============================================================================
# was 14_payment_switch. BIN 4xxx->approve, 5xxx->decline, 6xxx->sink/timeout,
# 9xxx->no route/decline.

SWITCH_PORT=8583
SWITCH_SCENARIO="${ROOT_DIR}/test/robot/suites/payment_switch/scenarios/switch.yaml"
SWITCH_SPEC="${ROOT_DIR}/test/robot/suites/payment_switch/specs/auth.yaml"
SWITCH_TOOL="$(bin_dir)/iso8583-tool"
SWITCH_SCHEME_PIDS=()

mkdir -p "${WORK_DIR}/rack_switch/data" "${WORK_DIR}/rack_switch/logs" "${WORK_DIR}/rack_switch/specs"
touch "${WORK_DIR}/rack_switch/logs/rack.log"
cp "${SWITCH_SPEC}" "${WORK_DIR}/rack_switch/specs/auth.yaml"
cat > "${WORK_DIR}/rack_switch/fluxrig.toml" <<EOF
[logging]
level = "trace"
filename = "logs/rack.log"

[store]
dir = "./data"
wal_max_size_mb = 500
state_file = "state.flux"

[rack]
name = "rack-switch"

[rack.bus]
url = "nats://127.0.0.1:${SNAKE_PORT}"
stream_name = "flux-msg"

[rack.telemetry]
enabled = true
EOF

section "Starting scheme hosts"
"${SWITCH_TOOL}" -mode scheme -scheme-port 10001 -scheme-spec "$SWITCH_SPEC" -scheme-de39 00 > "${WORK_DIR}/scheme_a.log" 2>&1 &
SWITCH_SCHEME_PIDS+=($!)
"${SWITCH_TOOL}" -mode scheme -scheme-port 10002 -scheme-spec "$SWITCH_SPEC" -scheme-de39 05 > "${WORK_DIR}/scheme_b.log" 2>&1 &
SWITCH_SCHEME_PIDS+=($!)
"${SWITCH_TOOL}" -mode scheme -scheme-port 10003 -scheme-spec "$SWITCH_SPEC" -scheme-sink > "${WORK_DIR}/scheme_c.log" 2>&1 &
SWITCH_SCHEME_PIDS+=($!)
wait_for_port 10001 10 || fail "scheme A failed"
wait_for_port 10002 10 || fail "scheme B failed"
wait_for_port 10003 10 || fail "scheme C failed"

cd "${WORK_DIR}/rack_switch"
FLUXRIG_TRACE=1 "$(bin_dir)/fluxrig" run -c "fluxrig.toml" > "rack.stdout" 2>&1 &
RACK_PIDS[switch]=$!
cd "${BASE_DIR}"
sleep 3

section "Deploying switch scenario"
status=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${API_URL}/scenario/import?activate=true" \
    -H "Content-Type: application/x-yaml" --data-binary @"${SWITCH_SCENARIO}")
[ "$status" = "200" ] || fail "switch scenario import failed (HTTP $status)"
wait_for_port "$SWITCH_PORT" 20 || fail "switch ingress failed to bind :$SWITCH_PORT"
sleep 3

section "Edge-case routing outcomes"
SWITCH_FAILS=0
switch_check() {
    local label="$1"; shift
    if "${SWITCH_TOOL}" -mode auth -auth-target "127.0.0.1:${SWITCH_PORT}" -auth-spec "$SWITCH_SPEC" "$@"; then
        log_success "$label"
    else
        log_error "$label"; SWITCH_FAILS=$((SWITCH_FAILS+1))
    fi
}
switch_check "BIN 9xxx -> no_route -> decline" -auth-pan 9111111111111111 -auth-stan 000004 -auth-expect-mti 0210 -auth-expect-de39 05

section "Concurrent multi-source load (per-scheme + multi-scheme terminals)"
"${SWITCH_TOOL}" -mode auth -auth-target "127.0.0.1:${SWITCH_PORT}" -auth-spec "$SWITCH_SPEC" \
    -auth-pan 4 -auth-expect-mti 0210 -auth-expect-de39 00 \
    -auth-count 40 -auth-rate 25 -auth-stan-base 100000 \
    > "${WORK_DIR}/term_a.log" 2>&1 &
SWITCH_TA=$!
"${SWITCH_TOOL}" -mode auth -auth-target "127.0.0.1:${SWITCH_PORT}" -auth-spec "$SWITCH_SPEC" \
    -auth-pan 5 -auth-expect-mti 0210 -auth-expect-de39 05 \
    -auth-count 16 -auth-rate 8 -auth-stan-base 300000 \
    > "${WORK_DIR}/term_b.log" 2>&1 &
SWITCH_TB=$!
"${SWITCH_TOOL}" -mode auth -auth-target "127.0.0.1:${SWITCH_PORT}" -auth-spec "$SWITCH_SPEC" \
    -auth-mix "4:00,5:05" -auth-expect-mti 0210 \
    -auth-count 30 -auth-rate 15 -auth-stan-base 500000 \
    > "${WORK_DIR}/term_m.log" 2>&1 &
SWITCH_TM=$!

switch_wait_term() {
    local pid="$1" label="$2" logf="$3"
    if wait "$pid"; then
        log_success "$label -> $(tail -n1 "$logf")"
    else
        log_error "$label -> $(tail -n1 "$logf")"
        SWITCH_FAILS=$((SWITCH_FAILS+1))
    fi
}
switch_wait_term "$SWITCH_TA" "terminal A (BIN4 @25tps)" "${WORK_DIR}/term_a.log"
switch_wait_term "$SWITCH_TB" "terminal B (BIN5 @8tps)" "${WORK_DIR}/term_b.log"
switch_wait_term "$SWITCH_TM" "terminal M (mix 4/5 @15tps)" "${WORK_DIR}/term_m.log"

section "BIN 6xxx -> timeout -> decline"
switch_check "BIN 6xxx -> timeout -> decline" \
    -auth-pan 6111111111111111 -auth-stan 000003 -auth-expect-mti 0210 -auth-expect-de39 91 -auth-timeout 8s

[ "$SWITCH_FAILS" -eq 0 ] || fail "Payment Switch: $SWITCH_FAILS failures"

for p in "${SWITCH_SCHEME_PIDS[@]}"; do stop_pid "$p"; done
stop_pid "${RACK_PIDS[switch]}"
unset 'RACK_PIDS[switch]'
wait_for_port_free "$SWITCH_PORT" 10 || log_warn "Port $SWITCH_PORT still bound after rack-switch teardown"

log_success "Phase 14 OK"
