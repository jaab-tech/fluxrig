# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     Shared setup for the ISO 8583 to ISO 20022 bridge suite: the
...               Mixer, the two Racks of the tutorial, and the artefacts the
...               scenario names, uploaded through the Mixer API in the order
...               the tutorial gives.
Library           fluxrigLibrary
Library           ConductorLibrary
Library           OperatingSystem
Library           Collections

*** Variables ***
${MIXER_PORT}       8090
# The ports of the tutorial's scenario, which this suite runs unchanged.
${TERMINALS_PORT}   8583
${RAIL_PORT}        7020
${EXAMPLES}         ${EMPTY}
${WORK_DIR}         ${EMPTY}

*** Keywords ***
Start Bridge Platform
    [Documentation]    Runs the tutorial's setup. The scenario and every spec
    ...                come from examples/, the files the tutorial copies, so
    ...                the suite fails when the tutorial and the code disagree.
    [Arguments]    ${suite_dir}
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${suite_dir}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}
    ${examples}=    Normalize Path    ${suite_dir}/../../../../examples
    Set Suite Variable    ${EXAMPLES}    ${examples}

    Generate Cluster Key    work_dir=${WORK_DIR}/mixer
    Start Mixer    config_file=${suite_dir}/configs/mixer/mixer.toml    work_dir=${WORK_DIR}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}

    # The protocol specs and the records first: the Mixer refuses a
    # translation spec that names an artefact it does not hold.
    Upload Spec    ${MIXER_PORT}    ${EXAMPLES}/specs/iso8583-v87-ascii.yaml
    Upload Spec    ${MIXER_PORT}    ${EXAMPLES}/specs/iso20022-rtp.yaml
    Upload Spec    ${MIXER_PORT}    ${EXAMPLES}/lookups/billers.yaml    name=billers    tag=v1
    Upload Spec    ${MIXER_PORT}    ${EXAMPLES}/specs/billpay-rtp.translation.yaml
    Upload Spec    ${MIXER_PORT}    ${EXAMPLES}/specs/rail-sim.translation.yaml

    FOR    ${r}    IN    switch    rail
        Start Rack    config_file=${suite_dir}/configs/racks/${r}.toml    work_dir=${WORK_DIR}/${r}    mixer_home=${WORK_DIR}/mixer    alias=rack-${r}
        Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=rack-${r}
    END

    # No spec file is copied to a Rack: each receives the artefacts with the
    # scenario, from the Mixer's store.
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${EXAMPLES}/scenarios/billpay_rtp.yaml
    Wait For Port    port=${RAIL_PORT}    timeout=30
    Wait For Port    port=${TERMINALS_PORT}    timeout=30
    # The conductor sends nothing to the rail until rail reports its link up.
    # rail logs the connection, so that is the condition, not a fixed wait.
    Wait Until Keyword Succeeds    30s    500ms    Rail Link Should Be Up

Rail Link Should Be Up
    [Documentation]    Succeeds once the rail client of rack-switch is connected.
    ${found}=    Grep File    ${WORK_DIR}/switch/logs/process_stdout.log    connected*127.0.0.1:${RAIL_PORT}
    Should Not Be Empty    ${found}    msg=rack-switch has not connected to the rail yet

Rail Link Should Be Down
    [Documentation]    Succeeds once rack-switch has seen the rail connection close.
    ${lost}=    Grep File    ${WORK_DIR}/switch/logs/process_stdout.log    connection lost
    ${dial}=    Grep File    ${WORK_DIR}/switch/logs/process_stdout.log    dial failed
    Should Be True    $lost or $dial    msg=rack-switch still has the rail connection

Pay A Bill
    [Documentation]    Sends one 0200 bill payment as the tutorial does, and
    ...                returns the report of the terminal.
    [Arguments]    ${stan}    ${amount}    ${merchant}
    ${report}=    Run Auth Terminal    127.0.0.1:${TERMINALS_PORT}
    ...    ${EXAMPLES}/specs/iso8583-v87-ascii.yaml    ${WORK_DIR}/pay_${stan}.json
    ...    mti=0200    proc_code=500000    amount=${amount}    currency=840
    ...    merchant_id=${merchant}    terminal_id=TERM0001    stan=${stan}    rrn=auto
    ...    expect_mti=0210
    RETURN    ${report}

Response Code Should Be
    [Documentation]    The terminal got exactly one 0210, with this DE 39.
    [Arguments]    ${report}    ${de39}
    ${codes}=    Get From Dictionary    ${report}    by_de39
    Should Be Equal As Integers    ${report}[total]    1    msg=the terminal sent ${report}[total] requests
    Should Be Equal As Integers    ${report}[ok]    1
    ...    msg=no 0210 came back: ${report}
    Dictionary Should Contain Key    ${codes}    ${de39}
    ...    msg=the 0210 had DE 39 ${codes}, not ${de39}
    Should Be Equal As Integers    ${codes}[${de39}]    1
    ...    msg=the 0210 had DE 39 ${codes}, not ${de39}

Stop Bridge Platform
    Stop All Processes
