*** Settings ***
Documentation     Force Log Coverage Suite
...               This suite runs fluxrig in various modes to trigger
...               log messages that are not covered by the standard happy path.
...               Goals:
...               1. Trigger Debug/Trace logs (via FLUXRIG_TRACE=true).
...               2. Trigger Error logs (via invalid configuration).

Library           Process
Library           OperatingSystem

*** Variables ***
${FLUXRIG_BIN}    ${CURDIR}/../../../../bin/fluxrig
${TIMEOUT}        5s

*** Test Cases ***
Trigger Debug Logs
    [Documentation]    Runs fluxrig with FLUXRIG_TRACE=true to emit debug logs.
    [Setup]    Remove File    rack.log
    ${env}=    Create Dictionary    FLUXRIG_TRACE=true    FLUXRIG_STDOUT_ENABLED=true
    Log    Starting fluxrig Rack help to emit Trace Logs...
    # Use 'rack' subcommand to ensure logger initialization
    ${result}=    Run Process    ${FLUXRIG_BIN}    rack    env=${env}    timeout=5s
    Log    fluxrig Trace Output:\n${result.stdout}
    
    # Standard format: 2026-04-07... | INFO | ...
    Should Contain    ${result.stdout}    | INFO |
    Should Contain    ${result.stdout}    | DEBUG |
    Should Contain    ${result.stdout}    | TRACE |

Trigger Config Error Logs
    [Documentation]    Runs fluxrig with invalid configuration to trigger error logs.
    ${env}=    Create Dictionary    FLUXRIG_NATS_URL=nats://invalid-host:4222
    Log    Starting fluxrig with Bad Config...
    ${result}=    Run Process    ${FLUXRIG_BIN}    rack    -c    non_existent.toml    env=${env}    timeout=${TIMEOUT}
    Log    fluxrig Bad Config Output:\n${result.stderr}
    # Cobra prints errors to stderr
    Should Contain    ${result.stderr}    Error: failed to load config
