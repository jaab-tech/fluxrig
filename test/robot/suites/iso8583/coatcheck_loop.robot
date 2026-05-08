*** Settings ***
Documentation     Coat Check Full Loop Validation
...               Validates that context (Source Connection ID) is preserved across a stateless hop.
...               Topology: LoadGen -> Gateway -> Store -> Client -> Echo -> Restore -> Gateway.
...               Without "Restore", the Gateway would fail to route the response because it lacks the "src_id".
Resource          ../../resources/common.resource
Library           fluxrigLibrary
Library           ISO8583Library
Library           Collections
Library           OperatingSystem
Library           Process
Suite Setup       Setup CoatCheck Suite
Suite Teardown    Teardown CoatCheck Suite

*** Variables ***
${MIXER_PORT}      8090
${WORK_DIR}        ${EMPTY}
${ISO_HOST}        localhost
${ISO_PORT}        8583
${ECHO_PORT}       54321

# Timelines (in seconds)
${T0_START}        0     # Worker 1 Start
${T1_START}        10    # Worker 2 Start (+10s)
${T2_START}        30    # Worker 3 Start (+20s from prev = T+30s)
${T3_END_W3}       90    # Worker 3 End (Duration 60s, so 30+60=90)
${T4_END_W2}       110   # Worker 2 End (Duration 100s, so 10+100=110)
${T5_END_W1}       120   # Worker 1 End (Duration 120s, so 0+120=120)

*** Keywords ***
Setup CoatCheck Suite
    [Arguments]    ${suite_path}=${CURDIR}
    Log    DEBUG: Starting Setup CoatCheck Suite
    Log    DEBUG: Suite Path is ${suite_path}
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${suite_path}    output_dir=${OUTPUT_DIR}
    Log    DEBUG: Work Dir returned is ${wd}
    Set Suite Variable    ${WORK_DIR}    ${wd}
    Set Suite Variable    ${MIXER_CONFIG}    ${suite_path}/configs/mixer/fluxrig-mixer.toml
    Set Suite Variable    ${RACK_CONFIG}     ${suite_path}/configs/rack/iso_rack.toml
    Set Suite Variable    ${SCENARIO_FILE}   ${suite_path}/configs/scenarios/scenario_coatcheck.yaml

    # Start Rack Agent
    Create Directory    ${WORK_DIR}/rack/logs
    Create Directory    ${WORK_DIR}/rack/data
    
    # Start Mixer
    Create Directory    ${WORK_DIR}/mixer/logs
    Create Directory    ${WORK_DIR}/mixer/data
    Generate Cluster Key   work_dir=${WORK_DIR}/mixer

    Start Mixer    config_file=${MIXER_CONFIG}    work_dir=${WORK_DIR}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}
    
    # Start Echo Server (External System)
    Start Echo Server    port=${ECHO_PORT}    log_file=${WORK_DIR}/echo_server.log

    # Import Scenario
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${SCENARIO_FILE}

    # Start Rack
    Start Rack    config_file=${RACK_CONFIG}    work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack
    
    # Wait for Rack Registration
    Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=iso-node-01
    Wait For Port    port=${ISO_PORT}    timeout=30

Teardown CoatCheck Suite
    # Wait for trailing events
    Sleep    5s
    
    # Stop Processes
    Stop Process    rack
    Stop Process    mixer
    Stop Process    echo_server
    Stop All Load Generators
    
    # Allow filesystem sync
    Sleep    5s
    Run Keyword And Continue On Failure    Check Log For Errors    ${WORK_DIR}/rack/logs/fluxrig.log
    Run Keyword And Continue On Failure    Check Log For Errors    ${WORK_DIR}/mixer/logs/process_stdout.log
    
    # Generate Report
    Create Directory    ${OUTPUT_DIR}
    Run Keyword And Continue On Failure    Generate Suite Summary Report    ${OUTPUT_DIR}/coatcheck_performance_summary.html    work_dir=${WORK_DIR}

*** Test Cases ***

Coat Check Staged Load (Full Loop)
    [Documentation]    Validates Coat Check Store/Restore performance under staged load.
    ...                Topology: LoadGen -> Gateway -> Store -> Client -> Echo -> Restore -> Gateway.
    [Tags]    perf    staged    coatcheck

    # --- Test Configuration Display ---
    ${config_html}=    Set Variable    <div style="margin-bottom: 20px;"><h3 style="margin-top:0;">Traffic Schedule (Coat Check Loop)</h3><table border="1" cellpadding="5" style="border-collapse: collapse; width: 100%; font-family: monospace; font-size: 13px;"><thead><tr style="background-color: #f3f4f6;"><th>Worker</th><th>Profile</th><th>Start (T+)</th><th>Duration</th><th>End (T+)</th></tr></thead><tbody><tr><td>Worker 1</td><td>1 TPS (1 Conn) - Low</td><td>00:00</td><td>120s</td><td>02:00</td></tr><tr><td>Worker 2</td><td>50 TPS (10 Conn) - Baseline</td><td>00:10</td><td>100s</td><td>01:50</td></tr><tr><td>Worker 3</td><td>200 TPS (50 Conn) - Stress</td><td>00:30</td><td>60s</td><td>01:30</td></tr></tbody></table></div>

    # --- Worker 1: 00:00 ---
    Log    Starting Worker 1 (Low) at T=0
    Start Load Generator    alias=w1    target=${ISO_HOST}:${ISO_PORT}    concurrency=1    rate=1    duration=125s    report_file=${WORK_DIR}/r_w1.json
    
    # --- Wait to T=10 ---
    Sleep    10s
    Log    Starting Worker 2 (Baseline) at T=10
    Start Load Generator    alias=w2    target=${ISO_HOST}:${ISO_PORT}    concurrency=10    rate=50    duration=95s    report_file=${WORK_DIR}/r_w2.json
    
    # --- Wait to T=30 (Total elapsed 30) ---
    Sleep    20s
    Log    Starting Worker 3 (Stress) at T=30
    Start Load Generator    alias=w3    target=${ISO_HOST}:${ISO_PORT}    concurrency=50    rate=200    duration=65s    report_file=${WORK_DIR}/r_w3.json
    
    # --- Wait to T=90 (Worker 3 ends) ---
    Sleep    60s
    Log    Worker 3 Should be finishing (T=90)
    
    # --- Wait to T=100 (Worker 2 ends) ---
    Sleep    10s
    Log    Worker 2 Should be finishing (T=100)
    
    # --- Wait to T=120 (Worker 1 ends) ---
    Sleep    20s
    Log    Worker 1 Should be finishing (T=120)
    
    # Stop and Collect
    ${r1}=    Stop Load Generator    w1
    ${r2}=    Stop Load Generator    w2 
    ${r3}=    Stop Load Generator    w3
    
    # Generate Report
    @{workers}=    Create List
    ...    ${{ {'path': '${WORK_DIR}/r_w1.json', 'label': 'W1: Low (1 TPS)', 'color': 'rgba(59, 130, 246, 0.7)'} }}
    ...    ${{ {'path': '${WORK_DIR}/r_w2.json', 'label': 'W2: Baseline (50 TPS)', 'color': 'rgba(16, 185, 129, 0.7)'} }}
    ...    ${{ {'path': '${WORK_DIR}/r_w3.json', 'label': 'W3: Stress (200 TPS)', 'color': 'rgba(239, 68, 68, 0.7)'} }}
    Record Multi Worker Result    ${workers}    name=Coat_Check_Staged_Load    description=${config_html}
