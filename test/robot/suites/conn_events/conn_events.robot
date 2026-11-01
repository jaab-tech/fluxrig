*** Settings ***
Documentation     Durable connection events: a terminal that sends nothing gets a
...               logon when it connects, and the event survives a short outage of
...               the Mixer. The age limits come from max_event_age in the scenarios.
Library           fluxrigLibrary
Library           ConnEventsLibrary
Library           OperatingSystem
Library           Process

Suite Setup       Start Platform
Suite Teardown    Stop Platform
Test Teardown     Close All Connections

*** Variables ***
${MIXER_PORT}     8090
${SERVER_PORT}    8888
${UPSTREAM_PORT}  18891
${RACK_NAME}      rack-switch
# max_event_age of the scenarios.
${MAX_AGE}        8

*** Test Cases ***
A Terminal That Sends Nothing Gets A Logon
    [Documentation]    The first frame on the connection comes from the server.
    Connect Terminal    ${SERVER_PORT}
    Logon Should Arrive    10

Each Connection Gets Its Own Logon
    [Documentation]    The event names its connection, so the second terminal does
    ...                not take the logon of the first, and the STAN moves on.
    Connect Terminal    ${SERVER_PORT}    first
    ${first}=    Logon Should Arrive    10    first
    Connect Terminal    ${SERVER_PORT}    second
    ${second}=    Logon Should Arrive    10    second
    No Frame Should Arrive    1    first
    Should Be Equal As Integers    ${second}    ${first + 1}

A Logon Waits For A Mixer That Returns Inside The Age Limit
    [Documentation]    The Rack parks the event and sends it when the Mixer returns.
    Stop Process    mixer
    Connect Terminal    ${SERVER_PORT}
    Start Platform Mixer
    Logon Should Arrive    ${MAX_AGE}

A Logon Is Dropped When The Mixer Returns Past The Age Limit
    [Documentation]    A logon for a connection this old may reach the wrong one.
    Stop Process    mixer
    Connect Terminal    ${SERVER_PORT}
    ${past}=    Evaluate    ${MAX_AGE} + 2
    No Frame Should Arrive    ${past}
    Start Platform Mixer
    No Frame Should Arrive    ${MAX_AGE}
    Connect Terminal    ${SERVER_PORT}    later
    Logon Should Arrive    10    later

A Client Gear Gets The Same Event
    [Documentation]    The logon leaves on the connection the client dialed, and again
    ...                on the next one.
    Listen As Upstream    ${UPSTREAM_PORT}
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${CURDIR}/scenarios/client_logon.yaml
    Accept Upstream    30
    Logon Should Arrive    10    upstream
    Close Connection    upstream
    Accept Upstream    30
    Logon Should Arrive    10    upstream

*** Keywords ***
Start Platform
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${CURDIR}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}
    Create Directory    ${WORK_DIR}/rack/specs
    Copy File    ${CURDIR}/../../../../examples/specs/iso8583-v87-ascii.yaml    ${WORK_DIR}/rack/specs/iso8583-v87-ascii.yaml
    Generate Cluster Key    work_dir=${WORK_DIR}/mixer
    Start Platform Mixer
    Start Rack    config_file=${CURDIR}/../conductor/configs/rack/rack.toml    work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack
    Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=${RACK_NAME}
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${CURDIR}/scenarios/server_logon.yaml
    Wait For Port    port=${SERVER_PORT}    timeout=30

Start Platform Mixer
    Start Mixer    config_file=${CURDIR}/../conductor/configs/mixer/mixer.toml    work_dir=${WORK_DIR}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}

Stop Platform
    Run Keyword And Ignore Error    Close All Connections
    Run Keyword And Ignore Error    Stop Process    rack
    Run Keyword And Ignore Error    Stop Process    mixer
