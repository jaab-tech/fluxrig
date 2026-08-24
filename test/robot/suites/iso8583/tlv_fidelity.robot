# Copyright 2026 JAAB Tech SAS, Uruguay
*** Settings ***
Documentation     EMV chip data fidelity through a full Rack.
...
...               A message carrying DE 55 is decoded into a fluxMsg, crosses the
...               bus as CBOR, and is rebuilt by a second gear. What comes back
...               is compared against what went in, byte for byte.
...
...               The unit tests prove the codec preserves TLV in memory. These
...               prove it survives the parts a unit test cannot reach: the
...               framing, the wire, the bus, and a second gear that never saw
...               the original bytes.
Resource          ../../resources/common.resource
Library           fluxrigLibrary
Library           ISO8583Library
Library           OperatingSystem
Library           String
Suite Setup       Initialize TLV Suite    ${CURDIR}
Suite Teardown    Teardown TLV Suite

*** Variables ***
${MIXER_PORT}       8090
${WORK_DIR}         ${EMPTY}

# A 0100 carrying PAN, STAN, terminal ID and DE 55 with four EMV tags.
# DE 55 holds 9F02 (amount) and 5F2A (currency), both declared by the spec, plus
# 9F1F, which the spec does not declare. That tag is the point of the suite: it
# is what a scheme sends and no switch models in full.
#
# The tags are in the order the encoder emits, so request and reply can be
# compared directly. See TLV Tag Order Is Canonicalized for what happens when
# they are not.
${MSG_CANONICAL}    3031303040200000008002003139303030343131313131313131313131313131313030303030315445524d303030313032315f2a0208589f02060000000005019f1f04deadbeef

# The same message with DE 55 holding the same three tags in a terminal's order
# rather than the encoder's. Byte for byte identical everywhere else.
${MSG_ARRIVAL_ORDER}    3031303040200000008002003139303030343131313131313131313131313131313030303030315445524d303030313032319f02060000000005019f1f04deadbeef5f2a020858

${TAG_UNDECLARED}   9f1f04deadbeef
${TAG_AMOUNT}       9f0206000000000501
${TAG_CURRENCY}     5f2a020858

*** Keywords ***
Initialize TLV Suite
    [Arguments]    ${suite_path}
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${suite_path}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}

    Set Suite Variable    ${MIXER_CONFIG}    ${suite_path}/configs/mixer/fluxrig-mixer.toml
    Set Suite Variable    ${RACK_CONFIG}     ${suite_path}/configs/rack/iso_rack.toml
    Set Suite Variable    ${SCENARIO_FILE}   ${suite_path}/scenario_tlv.yaml

    # The codec gears load specs/tlv.yaml relative to the Rack's CWD.
    Copy File    ${suite_path}/specs/tlv.yaml    ${WORK_DIR}/rack/specs/tlv.yaml

    Generate Cluster Key    work_dir=${WORK_DIR}/mixer
    Start Mixer    config_file=${MIXER_CONFIG}    work_dir=${WORK_DIR}/mixer    alias=mixer
    Start Rack     config_file=${RACK_CONFIG}     work_dir=${WORK_DIR}/rack    mixer_home=${WORK_DIR}/mixer    alias=rack

    Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=iso-node-01
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${SCENARIO_FILE}
    Wait For Port    port=${ISO_PORT}    timeout=60
    Sleep    2s    reason=Let the gear's internal loop come fully up

Teardown TLV Suite
    Stop All Processes
    Run Keyword And Continue On Failure    Check Log For Errors    ${WORK_DIR}/rack/logs/fluxrig.log

*** Test Cases ***

Undeclared TLV Tag Survives A Full Round Trip
    [Documentation]    The acceptance case. A message carrying a TLV tag the spec
    ...                does not declare is decoded, crosses the bus, is rebuilt by
    ...                a second gear, and must return byte for byte unchanged.
    [Tags]    tlv    fidelity
    ${reply}=    Send ISO Message    ${MSG_CANONICAL}    target=${ISO_HOST}:${ISO_PORT}
    Should Be Equal    ${reply}    ${MSG_CANONICAL}
    ...    msg=DE 55 did not survive the round trip intact

Undeclared Tag Is Present In The Reply
    [Documentation]    Stated separately from the byte comparison so a failure
    ...                names the cause. Byte equality proves this too, but says
    ...                only that something differed.
    [Tags]    tlv    fidelity
    ${reply}=    Send ISO Message    ${MSG_CANONICAL}    target=${ISO_HOST}:${ISO_PORT}
    Should Contain    ${reply}    ${TAG_UNDECLARED}
    ...    msg=The tag the spec does not declare was dropped or altered

TLV Tag Order Is Canonicalized
    [Documentation]    Marks where the fidelity above stops. Tags arriving in a
    ...                terminal's order come back sorted by hex tag: every value
    ...                survives, but DE 55 is not the bytes that arrived.
    ...
    ...                This is structural in the parser, not a setting. EMV gives
    ...                no meaning to the order of data objects inside DE 55, so
    ...                parsing downstream is unaffected. Anything that treats the
    ...                raw DE 55 blob as opaque bytes is: a MAC computed over the
    ...                message as it arrived will not verify over the one leaving.
    [Tags]    tlv    fidelity    known-limit
    ${reply}=    Send ISO Message    ${MSG_ARRIVAL_ORDER}    target=${ISO_HOST}:${ISO_PORT}

    Should Not Be Equal    ${reply}    ${MSG_ARRIVAL_ORDER}
    ...    msg=Arrival order now survives; this test and its documentation are stale

    # Nothing lost, only reordered.
    Should Contain    ${reply}    ${TAG_UNDECLARED}
    Should Contain    ${reply}    ${TAG_AMOUNT}
    Should Contain    ${reply}    ${TAG_CURRENCY}
    Length Should Be    ${reply}    ${{ len($MSG_ARRIVAL_ORDER) }}
    ...    msg=Reordering must not change the message length

    # And the reply is exactly the canonical form of the same message.
    Should Be Equal    ${reply}    ${MSG_CANONICAL}
    ...    msg=Reordered input must produce the same canonical output

Repeated Messages Stay Stable
    [Documentation]    Guards against state leaking between messages in the
    ...                composite, which caches subfields across unpacks.
    [Tags]    tlv    fidelity
    FOR    ${i}    IN RANGE    10
        ${reply}=    Send ISO Message    ${MSG_CANONICAL}    target=${ISO_HOST}:${ISO_PORT}
        Should Be Equal    ${reply}    ${MSG_CANONICAL}
        ...    msg=Message ${i} came back altered; parser state is leaking between messages
    END
