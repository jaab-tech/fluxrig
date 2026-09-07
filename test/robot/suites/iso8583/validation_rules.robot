# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     What a spec's semantic rules do to real traffic.
...
...               The unit tests prove the validator answers a message correctly.
...               These prove the answer reaches the wire: that a message a rule
...               rejects does not come back, that the same message in warn mode
...               does, and that the default leaves traffic exactly as it was.
...
...               Three ports carry the three modes against one spec and one
...               Rack, so the three answers are compared in the same run rather
...               than across three deployments.
Resource          ../../resources/common.resource
Library           fluxrigLibrary
Library           ISO8583Library
Library           OperatingSystem
Library           String
Suite Setup       Initialize Validation Suite    ${CURDIR}
Suite Teardown    Teardown Validation Suite

*** Variables ***
${MIXER_PORT}     8090
${PORT_OFF}       8601
${PORT_WARN}      8602
${PORT_ENFORCE}   8603
${WORK_DIR}       ${EMPTY}
# How long silence has to last to count as "no reply". A dropped message leaves
# the connection open and quiet, so proving the non-event needs a bound.
${SILENCE}        4

*** Keywords ***
Initialize Validation Suite
    [Arguments]    ${suite_path}
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${suite_path}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}

    Set Suite Variable    ${MIXER_CONFIG}    ${suite_path}/configs/mixer/fluxrig-mixer.toml
    Set Suite Variable    ${RACK_CONFIG}     ${suite_path}/configs/rack/iso_rack.toml
    Set Suite Variable    ${SCENARIO_FILE}   ${suite_path}/scenario_validation_rules.yaml

    # The codec gears load specs/rules.yaml relative to the Rack's CWD.
    Copy File    ${suite_path}/specs/rules.yaml    ${WORK_DIR}/rack/specs/rules.yaml

    Generate Cluster Key    work_dir=${WORK_DIR}/mixer
    Start Mixer    config_file=${MIXER_CONFIG}    work_dir=${WORK_DIR}/mixer    alias=mixer
    Start Rack     config_file=${RACK_CONFIG}     work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack
    Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=iso-node-01
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${SCENARIO_FILE}
    FOR    ${p}    IN    ${PORT_OFF}    ${PORT_WARN}    ${PORT_ENFORCE}
        Wait For Port    port=${p}    timeout=60
    END

    # A request the spec is satisfied with, and one missing DE 2 -- which is
    # mandatory on an 0200, and is the only difference between them.
    ${good}=    Build Iso Message    0200    f2=4111111111111111    f4=10000    f11=1    f41=TERM0001
    ${bad}=     Build Iso Message    0200    f4=10000    f11=1    f41=TERM0001
    Set Suite Variable    ${GOOD}    ${good}
    Set Suite Variable    ${BAD}     ${bad}

Teardown Validation Suite
    Stop All Processes
    Run Keyword And Continue On Failure    Check Log For Errors    ${WORK_DIR}/rack/logs/fluxrig.log

Rack Log
    ${log}=    Get File    ${WORK_DIR}/rack/logs/fluxrig.log
    RETURN    ${log}

*** Test Cases ***
A Valid Message Comes Back In Every Mode
    [Documentation]    The acceptance case, and the control for the two below: if
    ...                a mode rejected traffic the spec is satisfied with, every
    ...                other result here would mean nothing.
    [Tags]    validation    acceptance
    FOR    ${p}    IN    ${PORT_OFF}    ${PORT_WARN}    ${PORT_ENFORCE}
        ${reply}=    Send Iso Message    ${GOOD}    target=${ISO_HOST}:${p}
        Should Be Equal    ${reply}    ${GOOD}
        ...    msg=port ${p} did not return a message its own spec accepts
    END

Off Leaves Traffic Exactly As It Was
    [Documentation]    The default. A message accepted yesterday must not be
    ...                rejected today because the code was upgraded.
    [Tags]    validation    off
    ${reply}=    Send Iso Message    ${BAD}    target=${ISO_HOST}:${PORT_OFF}
    Should Be Equal    ${reply}    ${BAD}
    ...    msg=a rule fired on a port where validation is off

Warn Records The Violation And Lets The Message Through
    [Documentation]    How an operator finds out whether their spec matches their
    ...                traffic, before deciding to enforce it.
    [Tags]    validation    warn
    ${reply}=    Send Iso Message    ${BAD}    target=${ISO_HOST}:${PORT_WARN}
    Should Be Equal    ${reply}    ${BAD}    msg=warning rejected a message

    ${log}=    Rack Log
    Should Contain    ${log}    Message breaks a spec rule
    Should Contain    ${log}    decode-warn

Enforce Stops The Message
    [Documentation]    The message never reaches the encoder, so nothing is
    ...                framed back onto the connection. That silence is the
    ...                whole difference from the two modes above, against the
    ...                same spec and the same bytes.
    [Tags]    validation    enforce
    Send Iso Message Expecting No Reply    ${BAD}
    ...    target=${ISO_HOST}:${PORT_ENFORCE}    wait=${SILENCE}

The Rejection Says Which Spec And Which Rule
    [Documentation]    An operator reading the log should not have to go and find
    ...                out what was wrong with the message.
    [Tags]    validation    enforce
    ${log}=    Rack Log
    Should Contain    ${log}    validating
    Should Contain    ${log}    DE 2
    Should Contain    ${log}    mandatory

A Warning Check Does Not Stop A Message While Enforcing
    [Documentation]    Severity lives on the rule, not on the deployment. The
    ...                spec marks "Amount is positive" as warn, so a zero amount
    ...                is recorded and travels.
    ...
    ...                It also proves the amount is compared as a number: DE 4 is
    ...                twelve zero-padded digits, and a rule reading them as
    ...                characters would never find them to be zero.
    [Tags]    validation    enforce    severity
    ${zero}=    Build Iso Message    0200    f2=4111111111111111    f4=0    f11=1    f41=TERM0001
    ${reply}=   Send Iso Message    ${zero}    target=${ISO_HOST}:${PORT_ENFORCE}
    Should Be Equal    ${reply}    ${zero}    msg=a check marked warn rejected the message

    ${log}=    Rack Log
    Should Contain    ${log}    Amount is positive
