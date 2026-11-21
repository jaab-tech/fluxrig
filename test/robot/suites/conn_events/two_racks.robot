*** Settings ***
Documentation     Durable connection events across two Racks on one host. A gear
...               that runs on every Rack must see only its own connections, and a
...               reader on another Rack must survive a stop. The age limit comes
...               from max_event_age in the scenarios.
Library           fluxrigLibrary
Library           ConnEventsLibrary
Library           OperatingSystem
Library           Process

Suite Setup       Start Two Racks
Suite Teardown    Stop Two Racks
Test Teardown     Close All Connections

*** Variables ***
${MIXER_PORT}     8090
${SERVER_PORT}    8888
${RACK_A}         rack-switch
${RACK_B}         rack-b
${LOGONS}         10
# max_event_age of the scenarios.
${MAX_AGE}        8

*** Test Cases ***
A Gear On Two Racks Sees Only Its Own Connections
    [Documentation]    With one consumer for both Racks, about half of these logons
    ...                would reach the Rack that does not hold the connection.
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${CURDIR}/scenarios/logon_on_every_rack.yaml
    ${port_a}=    Bound Port    ${WORK_DIR}/rack/logs/fluxrig.log
    ${port_b}=    Bound Port    ${WORK_DIR}/rack_b/logs/fluxrig.log
    FOR    ${i}    IN RANGE    ${LOGONS}
        Connect Terminal    ${port_a}    on_a
        Logon Should Arrive    10    on_a
        Close Connection    on_a
        Connect Terminal    ${port_b}    on_b
        Logon Should Arrive    10    on_b
        Close Connection    on_b
    END

A Reader On Another Rack Greets The Terminal
    [Documentation]    The event crosses from one Rack to the other through the Mixer.
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${CURDIR}/scenarios/reader_on_another_rack.yaml
    Wait For Port    port=${SERVER_PORT}    timeout=30
    Connect Terminal    ${SERVER_PORT}
    Logon Should Arrive    10

A Reader That Returns Inside The Age Limit Gets What It Missed
    Stop Process    rack_b
    Connect Terminal    ${SERVER_PORT}
    Start Second Rack
    Logon Should Arrive    ${MAX_AGE}

A Reader That Returns Past The Age Limit Sends No Logon
    Stop Process    rack_b
    Connect Terminal    ${SERVER_PORT}
    ${past}=    Evaluate    ${MAX_AGE} + 2
    No Frame Should Arrive    ${past}
    Start Second Rack
    No Frame Should Arrive    ${MAX_AGE}
    Connect Terminal    ${SERVER_PORT}    later
    Logon Should Arrive    10    later

*** Keywords ***
Start Two Racks
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${CURDIR}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}
    FOR    ${rack}    IN    rack    rack_b
        Create Directory    ${WORK_DIR}/${rack}/specs
        Copy File    ${CURDIR}/../../../../examples/specs/iso8583-v87-ascii.yaml    ${WORK_DIR}/${rack}/specs/iso8583-v87-ascii.yaml
    END
    Generate Cluster Key    work_dir=${WORK_DIR}/mixer
    Start Mixer    config_file=${CURDIR}/../conductor/configs/mixer/mixer.toml    work_dir=${WORK_DIR}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}
    Start Rack    config_file=${CURDIR}/../conductor/configs/rack/rack.toml    work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack
    Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=${RACK_A}
    Start Second Rack

Start Second Rack
    Start Rack    config_file=${CURDIR}/configs/rack/rack_b.toml    work_dir=${WORK_DIR}/rack_b    mixer_home=${WORK_DIR}/mixer    alias=rack_b
    Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=${RACK_B}

Stop Two Racks
    Run Keyword And Ignore Error    Close All Connections
    Run Keyword And Ignore Error    Stop Process    rack_b
    Run Keyword And Ignore Error    Stop Process    rack
    Run Keyword And Ignore Error    Stop Process    mixer
