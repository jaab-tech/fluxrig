# Copyright 2025 JAAB Tech SAS, Uruguay
*** Settings ***
Documentation     ISO8583 Server Mode Validation (Internal Loopback)
...               Validates FluxRig in Server Mode by forwarding traffic back to the source.
...               Topology: [Load Gen] -> [Server Gear] -> (Loopback) -> [Server Gear] -> [Load Gen]
Resource          ../../resources/common.resource
Library           FluxRigLibrary
Library           ISO8583Library
Library           Collections
Suite Setup       Initialize Server Suite    ${CURDIR}
Suite Teardown    Teardown Server Suite

*** Variables ***
${MIXER_PORT}      8090
${WORK_DIR}        ${EMPTY}
${TPS}             1000
${DURATION}        60s

*** Keywords ***
Initialize Server Suite
    [Arguments]    ${suite_path}
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${suite_path}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}
    
    # Define Config Paths (local to this suite)
    Set Suite Variable    ${MIXER_CONFIG}    ${suite_path}/configs/mixer/fluxrig-mixer.toml
    Set Suite Variable    ${RACK_CONFIG}     ${suite_path}/configs/rack/iso_rack.toml
    Set Suite Variable    ${SCENARIO_FILE}   ${suite_path}/scenario_server_loopback.yaml
    
    # --- Start Mixer ---
    Generate Cluster Key   work_dir=${WORK_DIR}/mixer
    Start Mixer    config_file=${MIXER_CONFIG}    work_dir=${WORK_DIR}/mixer    alias=mixer
    # Start Rack
    Start Rack    config_file=${RACK_CONFIG}    work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack
    
    # Verify Health via CLI (Integrated Testing)
    FluxRig Check    config_file=${RACK_CONFIG}    work_dir=${WORK_DIR}/rack
    
    Sleep    5s    reason=Wait for Rack to initialize
    
    # Wait for Rack Registration
    Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=iso-node-01
    Log Registry Contents        mixer_port=${MIXER_PORT}

    # Import Scenario
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${SCENARIO_FILE}
    
    # Wait for Port listener (Now enabled by scenario config)
    Wait For Port    port=${ISO_PORT}    timeout=30
    
    # --- Wait for ISO8583 Gear to start listening ---
    Wait For Port    port=${ISO_PORT}    timeout=60
    # Technical Warm-Up: Ensure Gear internal loop is fully active
    Sleep    2s

Teardown Server Suite
    Stop All Processes
    Stop All Load Generators
    # Log Verification (Fail if ERROR/FATAL found)
    Run Keyword And Continue On Failure    Check Log For Errors    ${WORK_DIR}/rack/logs/fluxrig.log
    Run Keyword And Continue On Failure    Check Log For Errors    ${WORK_DIR}/mixer/logs/mixer.log
    
    # Generate final summary report
    Run Keyword And Continue On Failure    Generate Suite Summary Report    ${WORK_DIR}/suite_performance_summary.html    work_dir=${WORK_DIR}

*** Test Cases ***

Server Loopback Validation (Functional)
    [Documentation]    Verifies Server Mode logic and Header Preservation via loopback.
    [Tags]    validation    server
    ${report}=    Run Native Load Test    target=${ISO_HOST}:${ISO_PORT}    concurrency=5    rate=10    duration=5s    report_file=${WORK_DIR}/r_server_valid.json    warmup=1s
    Assert Response Rate Above   ${report}    100.0
    Assert Latency P99 Below     ${report}    50.0
    Sleep    2s    reason=Zero-Warning Stabilization: Wait for OTel flush
    Record Performance Result  ${WORK_DIR}/r_server_valid.json    name=Functional    description=Functional validation verifiying MTI 0800 loopback with BCD encoding and 12-byte correlation headers.    work_dir=${WORK_DIR}

Server Loopback Performance (Baseline 100 TPS)
    [Documentation]    Baseline performance test at 100 TPS.
    [Tags]    perf    baseline
    ${report}=    Run Native Load Test    target=${ISO_HOST}:${ISO_PORT}    concurrency=10    rate=100    duration=60s    report_file=${WORK_DIR}/r_server_100tps.json    warmup=2s
    Assert Response Rate Above   ${report}    95.0
    Assert Latency P99 Below     ${report}    100.0
    Sleep    2s    reason=Zero-Warning Stabilization: Wait for OTel flush
    Record Performance Result  ${WORK_DIR}/r_server_100tps.json    name=Baseline    description=Baseline performance measurement at 100 Transactons Per Second with 10 parallel connections.    work_dir=${WORK_DIR}

Server Loopback Performance (Stress 1000 TPS)
    [Documentation]    High-load stress test at 1000 TPS to identify system saturation points.
    [Tags]    perf    stress
    ${report}=    Run Native Load Test    target=${ISO_HOST}:${ISO_PORT}    concurrency=10    rate=${TPS}    duration=${DURATION}    report_file=${WORK_DIR}/r_server_1000tps.json    warmup=5s
    Assert Response Rate Above   ${report}    80.0
    # Latency warning threshold
    ${p99}=    Get From Dictionary    ${report}    latency_p99_ms
    Run Keyword If    ${p99} > 200    Log    Critical Latency Detected: ${p99}ms    WARN
    Sleep    2s    reason=Zero-Warning Stabilization: Wait for OTel flush
    Record Performance Result  ${WORK_DIR}/r_server_1000tps.json    name=Stress    description=High-load stress test at 1000 TPS with 10 concurrent connections over 60 seconds to identify system saturation points.    work_dir=${WORK_DIR}
