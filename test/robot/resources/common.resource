Library    ISO8583Library
Library    fluxrigLibrary

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
    [Documentation]    Stops any active artifacts and processes
    Run Keyword And Ignore Error    Stop Echo Server
    Run Keyword And Ignore Error    Stop All Load Generators
    Run Keyword And Ignore Error    Stop All Processes
