*** Settings ***
Documentation     API Verification Tests for FluxRig Mixer
Library           RequestsLibrary
Library           Collections

*** Variables ***
${BASE_URL}       http://localhost:8090/api/v1
${HEADERS}        Create Dictionary    Content-Type=application/json

*** Test Cases ***
Verify Health Endpoint
    [Documentation]    Check if the health endpoint returns status ok and version.
    Create Session    mixer    ${BASE_URL}
    ${response}=      GET On Session    mixer    /health
    Should Be Equal As Strings    ${response.status_code}    200
    ${json}=          Set Variable    ${response.json()}
    Dictionary Should Contain Key    ${json}    status
    Dictionary Should Contain Key    ${json}    version
    Should Be Equal    ${json}[status]    ok

Verify Racks List Is Initially Empty
    [Documentation]    The racks list should be empty on a fresh start (assuming fresh DB).
    Create Session    mixer    ${BASE_URL}
    ${response}=      GET On Session    mixer    /racks
    Should Be Equal As Strings    ${response.status_code}    200
    ${list}=          Set Variable    ${response.json()}
    Should Be Empty    ${list}

Verify Approve Invalid Rack Returns 404
    [Documentation]    Attempting to approve a non-existent rack ID should fail.
    Create Session    mixer    ${BASE_URL}
    ${body}=          Create Dictionary    name=new-rack
    ${response}=      POST On Session    mixer    /racks/999/approve    json=${body}    expected_status=404
