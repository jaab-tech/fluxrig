*** Settings ***
Library    ../lib/FluxRigLibrary.py
Library    ../lib/NATSLibrary.py
Library    ../lib/ToxiProxyLibrary.py
Library    ../lib/TelemetryLibrary.py

*** Variables ***
${MIXER_CONFIG}    ${CURDIR}/../mixer.toml
${RACK_CONFIG}     ${CURDIR}/../rack.toml

*** Keywords ***
Teardown Test Environment
    [Documentation]    Cleanup after test
    Stop All Processes
    Close Nats
