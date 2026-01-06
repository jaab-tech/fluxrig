*** Settings ***
Documentation     Orchestration Scenarios for FluxRig (Mixer + multiple Racks)
Library           Process
Library           RequestsLibrary
Library           Collections
Library           OperatingSystem

Suite Setup       Setup Test Environment
Suite Teardown    Teardown Test Environment

*** Variables ***
${MIXER_BIN}      ./bin/fluxrig-mixer
${RACK_BIN}       ./bin/fluxrig
${CONFIG_PATH}    test/robot/mixer.toml
${BASE_URL}       http://localhost:8090/api/v1
${DB_PATH}        data/fluxrig_test.duckdb

*** Test Cases ***

Scenario: Multi-Rack Registration
    [Documentation]    Start Mixer and 3 Racks, verify they all register.
    
    # 1. Start Mixer (handled in Suite Setup)
    Wait Until Keyword Succeeds    10x    1s    Check Mixer Health

    # 2. Start 3 Racks
    Start Rack    rack1    node-one
    Start Rack    rack2    node-two
    Start Rack    rack3    node-three

    # 3. Wait for Registration (Heartbeats should trigger Upsert)
    Wait Until Keyword Succeeds    30x    1s    Verify Racks Count    3

    # 4. Verify Details via API
    Verify Rack Status    node-one      pending    # Since it starts with node-, it is pending
    Verify Rack Status    node-two      pending
    Verify Rack Status    node-three    pending

Scenario: Heartbeat Updates Last Seen
    [Documentation]    Verify that last_seen updates for a rack.
    
    # Get initial time for rack1
    ${initial}=    Get Rack Last Seen    node-one
    Sleep          2s
    # Rack sends heartbeat every 1s (we need to config this) or default 30s.
    # We should override config for test!
    
    # For this test, we rely on the fact that we just started it.
    # To test heartbeat logic, we need to ensure interval is short.
    # In Suite Setup, we set FLUXRIG_RACK_HEARTBEAT_INTERVAL=1s
    
    ${later}=      Get Rack Last Seen    node-one
    Should Not Be Equal    ${initial}    ${later}

*** Keywords ***

Setup Test Environment
    # Remove old Data (DB + NATS)
    Remove Directory    data    recursive=True
    
    # Start Mixer
    ${mixer}=    Start Process    ${MIXER_BIN}    -c    ${CONFIG_PATH}    alias=mixer    stdout=test/test_logs/mixer.log    stderr=test/test_logs/mixer.err
    Set Suite Variable    ${MIXER_HANDLE}    ${mixer}
    
    # Initialize Requests
    Create Session    mixer    ${BASE_URL}

Teardown Test Environment
    Terminate All Processes    kill=True

Check Mixer Health
    ${resp}=    GET On Session    mixer    /health
    Should Be Equal As Strings    ${resp.status_code}    200

Start Rack
    [Arguments]    ${alias}    ${name}
    # We inject config via ENV
    ${env}=    Create Dictionary    FLUXRIG_RACK_BUS_URL=nats://localhost:4223    FLUXRIG_RACK_NAME=${name}    FLUXRIG_RACK_HEARTBEAT_INTERVAL=1s
    Start Process    ${RACK_BIN}    rack    env=${env}    alias=${alias}    stdout=test/test_logs/${alias}.log    stderr=test/test_logs/${alias}.err

Verify Racks Count
    [Arguments]    ${expected_count}
    ${resp}=    GET On Session    mixer    /racks
    ${list}=    Set Variable    ${resp.json()}
    ${length}=  Get Length    ${list}
    Should Be Equal As Integers    ${length}    ${expected_count}

Verify Rack Status
    [Arguments]    ${name}    ${expected_status}
    ${resp}=    GET On Session    mixer    /racks
    ${list}=    Set Variable    ${resp.json()}
    
    # Find item
    ${found}=    Set Variable    ${False}
    FOR    ${item}    IN    @{list}
        IF    '${item}[name]' == '${name}'
            Should Be Equal    ${item}[status]    ${expected_status}
            ${found}=    Set Variable    ${True}
            BREAK
        END
    END
    Should Be True    ${found}    Rack ${name} not found in list

Get Rack Last Seen
    [Arguments]    ${name}
    ${resp}=    GET On Session    mixer    /racks
    ${list}=    Set Variable    ${resp.json()}
    FOR    ${item}    IN    @{list}
        IF    '${item}[name]' == '${name}'
            RETURN    ${item}[last_seen]
        END
    END
    Fail    Rack ${name} not found
