*** Settings ***
Documentation     End-to-End Test for Wasm Gear (Zig Polyglot)
...               Verifies that a dynamically loaded Wasm module can process fluxMsgs.
Library           OperatingSystem
Library           String
Library           Process
Resource          ../../robot/resources/common.resource
Library           fluxrigLibrary

Suite Setup       Initialize Wasm Suite
Suite Teardown    Teardown Test Environment

*** Variables ***
${MIXER_PORT}     8090
${WORK_DIR}       ${EMPTY}
${WASM_FILE}      ${CURDIR}/build/polyglot.wasm
${SCENARIO}       ${CURDIR}/scenario.yaml

*** Test Cases ***
Verify Strict Validation Rejects Unknown Vendor
    [Documentation]    Ensure the Mixer rejects Wasm modules signed by an unknown vendor
    
    # Check if Wasm file exists, if not, skip test or try building
    ${file_exists}=    Run Keyword And Return Status    File Should Exist    ${WASM_FILE}
    Run Keyword If    not ${file_exists}    Fail    Wasm file not found. Please run build.sh first!

    Log    Generating Rogue Vendor Key...
    Create Directory    ${WORK_DIR}/rogue_keys
    Run Process    go    run    ./cmd/fluxrig    keys    gen-cluster    -o    ${WORK_DIR}/rogue_keys/cluster.key    cwd=${CURDIR}/../../..
    
    Log    Starting Mixer...
    Generate Cluster Key   work_dir=${WORK_DIR}/mixer
    Start Mixer    config_file=${MIXER_CONFIG}    work_dir=${WORK_DIR}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}

    Log    Signing Wasm Payload with Rogue Key...
    Run Process    go    run    ./cmd/fluxrig    wasm    sign    ${WASM_FILE}    ${WORK_DIR}/rogue_keys/cluster.key    cwd=${CURDIR}/../../..

    Log    Attempting to Import Rogue Wasm Payload...
    ${result}=    Run Process    go    run    ./cmd/fluxrig    wasm    import    ${WASM_FILE}    cwd=${CURDIR}/../../..
    Log    stdout: ${result.stdout}
    Log    stderr: ${result.stderr}
    Should Contain    ${result.stderr}    vendor signature found but does not match any trusted keys
    Should Not Be Equal As Integers    ${result.rc}    0
    
    Log    Stopping Mixer to reset state...
    Stop Process    alias=mixer
    Sleep    2s

Verify Wasm Gear Loads And Processes
    [Documentation]    Ensure the Zig Wasm gear processes messages successfully
    
    # 0. Set up Vendor Keys for Strict Wasm Import
    Log    Generating Vendor Key...
    Create Directory    ${WORK_DIR}/vendor_keys
    Run Process    go    run    ./cmd/fluxrig    keys    gen-cluster    -o    ${WORK_DIR}/vendor_keys/cluster.key    cwd=${CURDIR}/../../..
    Create Directory    ${WORK_DIR}/mixer/data/wasm/keys
    Copy File    ${WORK_DIR}/vendor_keys/cluster.key.pub    ${WORK_DIR}/mixer/data/wasm/keys/my_vendor.pub

    # 1. Start Mixer and Rack
    Log    Starting Mixer...
    Generate Cluster Key   work_dir=${WORK_DIR}/mixer
    Start Mixer    config_file=${MIXER_CONFIG}    work_dir=${WORK_DIR}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}

    Log    Starting Rack...
    Start Rack    config_file=${RACK_CONFIG}    work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack
    
    ${rack_id}=    Wait For Pending Rack    mixer_port=${MIXER_PORT}
    Adopt Rack     mixer_port=${MIXER_PORT}    machine_id=${rack_id}    name=rack
    
    # 2. Wasm Supply Chain Security
    Log    Signing Wasm Payload...
    # Sign with our actual Vendor Key
    Run Process    go    run    ./cmd/fluxrig    wasm    sign    ${WASM_FILE}    ${WORK_DIR}/vendor_keys/cluster.key    cwd=${CURDIR}/../../..
    
    Log    Importing Wasm Payload to Catalog (Strict Mode)...
    ${result}=    Run Process    go    run    ./cmd/fluxrig    wasm    import    ${WASM_FILE}    cwd=${CURDIR}/../../..
    Log    stdout: ${result.stdout}
    Log    stderr: ${result.stderr}
    Should Contain    ${result.stdout}    Successfully imported Wasm module
    
    # Extract hash (very naive matching, assumes 'Hash: <hash>')
    ${hash}=    Evaluate    re.search(r'Hash: ([a-f0-9]+)', '''${result.stdout}''').group(1)    modules=re
    
    # 3. Create active scenario with the hash and import it
    Log    Creating runtime scenario...
    ${scenario_content}=    Get File    ${SCENARIO}
    ${scenario_content}=    Replace String    ${scenario_content}    file:///Users/andresa/git/fluxrig/test/e2e/12_wasm_polyglot/build/polyglot.wasm    snake://wasm_catalog/${hash}.wasm
    Create File    ${WORK_DIR}/scenario_active.yaml    ${scenario_content}
    
    Log    Importing Scenario...
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${WORK_DIR}/scenario_active.yaml

    # 3. Restart Rack to pick up the Wasm gear from the scenario
    Log    Restarting Rack to pick up Gear...
    Stop Process    alias=rack
    Start Rack    config_file=${RACK_CONFIG}    work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack

    # 4. Inject a test message and verify response via CLI
    # Wasm gear will process it and return the payload.
    # Wasm gear logs "Zig Wasm Gear: Process called!"
    Log    Waiting for Wasm gear to process messages...
    Sleep    5s
    
    # Verify the Rack logs for the Wasm output
    ${logs}=    Grep File    ${WORK_DIR}/rack/logs/fluxrig.log    Zig Wasm Gear: Process called!
    Should Not Be Empty    ${logs}    Rack failed to execute Wasm process function

*** Keywords ***
Initialize Wasm Suite
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${CURDIR}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}
    
    # We can reuse configs from the topology suite
    Set Suite Variable    ${MIXER_CONFIG}    ${CURDIR}/../../robot/suites/topology/configs/mixer/fluxrig-mixer.toml
    Set Suite Variable    ${RACK_CONFIG}     ${CURDIR}/../../robot/suites/topology/configs/rack/cattle.toml
