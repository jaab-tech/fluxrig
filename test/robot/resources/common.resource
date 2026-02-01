*** Settings ***
Library    ../lib/ISO8583Library.py

*** Variables ***
${ISO_HOST}      localhost
${ISO_PORT}      8583
${MOCK_PORT}     8590
${REPORT_DIR}    ${CURDIR}/../results    # Robot Framework global results

*** Keywords ***
Setup Test Environment
    [Documentation]    Starts the Go Echo Server for Performance tests
    Start Echo Server  port=${MOCK_PORT}

Teardown Test Environment
    [Documentation]    Stops any active artifacts
    Stop Echo Server
    Stop All Load Generators
