# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     What the store can be asked about what it holds, from the CLI
...               and from the Mixer.
...
...               A reference alone is thin: an operator looking at a fleet needs
...               to know when a version was filed, which one `latest` reaches,
...               and what else exists under that name. And the listing has to
...               say what kind of thing it is listing -- specs and scenarios
...               live in one index, and both listings showed all of it, so a
...               scenario appeared as a spec and nothing errored.
Library           SpecLibrary
Library           fluxrigLibrary
Library           OperatingSystem
Library           Collections
Library           String
Resource          ../../resources/common.resource
Suite Setup       Fill A Store With A History
Suite Teardown    Teardown Test Environment

*** Variables ***
${MIXER_PORT}     8096
${API}            http://127.0.0.1:8096/api/v1

*** Keywords ***
Fill A Store With A History
    [Documentation]    Four versions of one spec, imported out of order on
    ...                purpose, plus a scenario -- the two things a listing has
    ...                to keep apart.
    ${work}=    Setup Workspace    ${CURDIR}
    Set Suite Variable    ${STORE}    ${work}/mixer/data/store

    ${spec}=    Repository Path    examples    specs    iso8583-v87-ascii.yaml
    ${base}=    Read File Text    ${spec}
    FOR    ${v}    IN    1.0.0    2.0.0    1.1.0    1.0.1
        ${body}=    Replace String Using Regexp    ${base}    (?m)^ +version: .*    ${SPACE}${SPACE}version: "${v}"
        ${path}=    Write Spec    ${work}/fixtures/v${v}.yaml    ${body}
        Run Fluxrig    spec    import    ${path}    --store-dir    ${STORE}
    END

    ${scn}=    Repository Path    examples    scenarios    roaming_enrichment.yaml
    Run Fluxrig    scenario    import    ${scn}    --store-dir    ${STORE}

    Generate Cluster Key    work_dir=${work}/mixer
    Start Mixer    config_file=${CURDIR}/configs/mixer/fluxrig-mixer-listing.toml
    ...            work_dir=${work}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}

*** Test Cases ***
The CLI Lists Specs And Not Scenarios
    [Documentation]    Both listings printed the whole index, so `spec list`
    ...                showed a scenario as a spec. Nothing errored; it answered
    ...                wrongly, which is worse.
    [Tags]    listing    cli
    ${res}=    Run Fluxrig    spec    list    --store-dir    ${STORE}
    Should Contain       ${res}[stdout]    iso8583-v87-ascii
    Should Not Contain   ${res}[stdout]    roaming_enrichment

    ${res}=    Run Fluxrig    scenario    list    --store-dir    ${STORE}
    Should Contain       ${res}[stdout]    roaming_enrichment
    Should Not Contain   ${res}[stdout]    iso8583-v87-ascii

The CLI Listing Carries The Attributes The Store Kept
    [Tags]    listing    cli
    ${res}=    Run Fluxrig    spec    list    --store-dir    ${STORE}
    Should Contain    ${res}[stdout]    IMPORTED
    Should Contain    ${res}[stdout]    SIZE
    Should Contain    ${res}[stdout]    TITLE
    Should Contain    ${res}[stdout]    ISO 8583:1987 (ASCII)
    Should Contain    ${res}[stdout]    (latest)

The CLI History Is Ordered By Version Not By Arrival
    [Documentation]    v1.0.1 was imported last and is not the newest. Ordering a
    ...                history by import time would put a patch to an old branch
    ...                above the release it does not contain.
    [Tags]    listing    cli    versioning
    ${res}=      Run Fluxrig    spec    history    iso8583-v87-ascii    --store-dir    ${STORE}    --json
    ${items}=    Parse Json    ${res}[stdout]
    ${tags}=     Versions In Order    ${items}
    Should Be Equal    ${tags}    ${{ ["v2.0.0", "v1.1.0", "v1.0.1", "v1.0.0"] }}

    ${newest}=    Set Variable    ${items}[0]
    Should Be True    ${newest}[latest]    msg=`latest` does not follow the highest version

The CLI History Of Something The Store Does Not Hold
    [Tags]    listing    cli    negative
    ${res}=    Run Fluxrig    spec    history    no-such-spec    --store-dir    ${STORE}    expect_failure=${True}
    Should Contain    ${res}[output]    no-such-spec

The API Lists Specs And Not Scenarios
    [Tags]    listing    api
    ${res}=      Http Get    ${API}/specs
    Should Not Contain    ${res}[body]    roaming_enrichment
    ${items}=    Parse Json    ${res}[body]
    Length Should Be    ${items}    4    The listing should hold four versions of one spec

The API Listing Carries The Attributes The Store Kept
    [Tags]    listing    api
    ${res}=      Http Get    ${API}/specs
    ${items}=    Parse Json    ${res}[body]
    ${first}=    Set Variable    ${items}[0]
    Should Be Equal    ${first}[title]    ISO 8583:1987 (ASCII)
    Should Be True     ${first}[size] > 0
    # The reference spec declares no `spec.protocol` and relies on the loader's
    # default, so the key is absent rather than guessed at. Absent is a fact a
    # consumer can act on; a filled-in default would be this listing inventing
    # something the document never said.
    Dictionary Should Not Contain Key    ${first}    protocol
    Dictionary Should Contain Key    ${first}    imported_at
    # A listing that made the reader assemble the doc URL would be one they have
    # to read the routing table to use.
    Should Contain    ${first}[doc]    /doc

The API Serves The History Of One Spec
    [Tags]    listing    api    versioning
    ${res}=      Http Get    ${API}/specs/iso8583-v87-ascii
    ${items}=    Parse Json    ${res}[body]
    ${tags}=     Versions In Order    ${items}
    Should Be Equal    ${tags}    ${{ ["v2.0.0", "v1.1.0", "v1.0.1", "v1.0.0"] }}

The Doc Link In A Listing Is One That Works
    [Documentation]    Following what the listing offers has to reach the
    ...                reference for that exact version, not for whatever
    ...                `latest` happens to be.
    [Tags]    listing    api    doc
    ${res}=      Http Get    ${API}/specs/iso8583-v87-ascii
    ${items}=    Parse Json    ${res}[body]
    ${old}=      Set Variable    ${items}[3]
    Should Be Equal    ${old}[tag]    v1.0.0
    ${doc}=    Http Get    http://127.0.0.1:8096${old}[doc]
    Should Contain    ${doc}[body]    Version 1.0.0

The API History Of An Unknown Spec Is Not Found
    [Tags]    listing    api    negative
    ${res}=    Http Get    ${API}/specs/no-such-spec    expect_status=404
    Should Contain    ${res}[body]    no-such-spec
