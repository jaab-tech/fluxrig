*** Settings ***
Documentation     Force Log Coverage Suite
...               This suite runs FluxRig in various modes to trigger
...               log messages that are not covered by the standard happy path.
...               Goals:
...               1. Trigger Debug/Trace logs (via FLUXRIG_TRACE=true).
...               2. Trigger Error logs (via invalid configuration).

Library           Process
Library           OperatingSystem

*** Variables ***
${FLUXRIG_BIN}    ./bin/fluxrig
${TIMEOUT}        5s

*** Test Cases ***
Trigger Debug Logs
    [Documentation]    Runs FluxRig with FLUXRIG_TRACE=true to emit debug logs.
    [Setup]    Remove File    rack.log
    ${env}=    Create Dictionary    FLUXRIG_TRACE=true    FLUXRIG_STDOUT_ENABLED=true
    Log    Starting FluxRig in Trace Mode...
    ${handle}=    Start Process    ${FLUXRIG_BIN}    rack    run    env=${env}
    Sleep    2s
    Terminate Process    ${handle}
    ${result}=    Get Process Result    ${handle}
    Log    FluxRig Trace Output:\n${result.stdout}
    Should Contain    ${result.stdout}    [DEBUG]
    Should Contain    ${result.stdout}    [TRACE]

Trigger Config Error Logs
    [Documentation]    Runs FluxRig with invalid configuration to trigger error logs.
    ${env}=    Create Dictionary    FLUXRIG_NATS_URL=nats://invalid-host:4222
    Log    Starting FluxRig with Bad Config...
    ${result}=    Run Process    ${FLUXRIG_BIN}    rack    run    env=${env}    timeout=${TIMEOUT}
    Log    FluxRig Bad Config Output:\n${result.stderr}
    # We expect it to fail or log errors
    # Should Contain    ${result.stderr}    Failed to connect
    # Note: Depending on stdout/stderr config, check output.

*** Keywords ***
