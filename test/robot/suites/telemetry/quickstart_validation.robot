# Copyright 2026 JAAB Tech SAS, Uruguay
*** Settings ***
Documentation     Quickstart Flow Validation
...               Validates the Startup Scenario logic and --auto-adopt behavior.
...               This mimics the user's "5-Minute Quickstart" path.
Resource          ../../resources/common.resource
Library           fluxrigLibrary
Library           OperatingSystem
Library           Collections
Suite Setup       Setup Quickstart Suite
Suite Teardown    Teardown Test Environment

*** Variables ***
${MIXER_PORT}      8090
${WORK_DIR}        ${EMPTY}
${SCENARIO_PATH}   ${CURDIR}/../../../../examples/scenarios/getting_started.yaml

*** Keywords ***
Setup Quickstart Suite
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${CURDIR}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}
    # Mixer Config
    Set Suite Variable    ${MIXER_CONFIG}    ${CURDIR}/configs/mixer/quickstart-mixer.toml
    # Rack Config (Cattle mode)
    Set Suite Variable    ${RACK_CONFIG}     ${CURDIR}/configs/rack/cattle.toml

*** Test Cases ***
Verify Startup Scenario Activation
    [Documentation]    Starts Mixer with --auto-adopt and --scenario flags.
    ...                Verifies that the scenario is active and gears are registered.
    
    # 1. Start Mixer with Startup Scenario
    @{mixer_flags}=    Create List    --auto-adopt    ${SCENARIO_PATH}
    Start Mixer    config_file=${MIXER_CONFIG}    work_dir=${WORK_DIR}/mixer    alias=mixer    extra_args=${mixer_flags}
    Wait For Healthy    port=${MIXER_PORT}
    
    # 2. Verify Scenario Metadata in Registry
    # The scenario name in getting_started.yaml is "Getting Started"
    Wait Until Keyword Succeeds    10s    1s    Verify Scenario Active    mixer_port=${MIXER_PORT}    scenario_name=Getting Started

    # 3. Start Rack (Should be auto-adopted and receive scenario)
    Start Rack    config_file=${RACK_CONFIG}    work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack
    
    # 4. Verify Rack is Active immediately
    # Since we don't know the exact name (cattle), we wait for any active rack
    Wait Until Keyword Succeeds    10s    1s    Check At Least One Rack Active    mixer_port=${MIXER_PORT}

    # 5. Verify Bento Gear is running and generating metrics
    # The gear name in getting_started.yaml is "generator"
    # We wait for the 'flux.gear.messages_out' metric to appear
    Log    Waiting for Bento generator metrics...
    Wait Until Keyword Succeeds    30s    2s    Verify Any Entity Metric Present    mixer_port=${MIXER_PORT}    metric_key=flux.gear.messages_out

    # 6. Verify wire message delivery (sink gear receives from generator via wire)
    # The Rack's process_stdout.log should contain the sink reading messages
    Sleep    5s
    ${stdout}=    Get File    ${WORK_DIR}/rack/logs/process_stdout.log
    Should Contain    ${stdout}    Bento Input Read
    Should Contain    ${stdout}    Bento Output Emitted

    # 7. Verify bidirectional gear metrics (sink receives messages_in)
    Wait Until Keyword Succeeds    30s    2s    Verify Any Entity Metric Present    mixer_port=${MIXER_PORT}    metric_key=flux.gear.messages_in
