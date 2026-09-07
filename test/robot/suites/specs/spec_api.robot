# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     The Mixer serving the specs its store holds, and the protocol
...               reference for each.
...
...               A spec is a deployed artefact. The one an operator wants to
...               read is the one that is running, and that is exactly the one
...               they cannot open from their own disk -- so the control plane
...               that holds it is what has to serve it.
...
...               The reference is derived on every request rather than stored
...               beside the spec. A document kept alongside its source is a
...               document that disagrees with it, and nothing about a stale
...               protocol reference looks wrong until someone acts on it.
Library           SpecLibrary
Library           fluxrigLibrary
Library           OperatingSystem
Resource          ../../resources/common.resource
Suite Setup       Start A Mixer Holding One Spec
Suite Teardown    Teardown Test Environment

*** Variables ***
${MIXER_PORT}     8095
${API}            http://127.0.0.1:8095/api/v1
${URN}            iso8583-v87-ascii/v2.2.0

*** Keywords ***
Start A Mixer Holding One Spec
    [Documentation]    The spec is imported before the Mixer boots, because the
    ...                store is opened at startup and this suite is about serving
    ...                what is already there.
    ${work}=    Setup Workspace    ${CURDIR}
    Set Suite Variable    ${WORK_DIR}    ${work}

    ${spec}=    Repository Path    examples    specs    iso8583-v87-ascii.yaml
    Set Suite Variable    ${SPEC_FILE}    ${spec}
    Run Fluxrig    spec    import    ${spec}    --store-dir    ${work}/mixer/data/store

    Generate Cluster Key    work_dir=${work}/mixer
    Start Mixer    config_file=${CURDIR}/configs/mixer/fluxrig-mixer.toml
    ...            work_dir=${work}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}

*** Test Cases ***
The Store Lists What It Holds
    [Tags]    api
    ${res}=    Http Get    ${API}/specs
    Should Contain    ${res}[body]    iso8583-v87-ascii
    Should Contain    ${res}[body]    v2.2.0
    Should Contain    ${res}[body]    "urn":"iso8583-v87-ascii:v2.2.0"

A Stored Spec Comes Back Byte For Byte
    [Documentation]    Content-addressed means the bytes are the identity. If the
    ...                store re-serialised what it holds, the hash it files
    ...                things under would stop describing what it returns.
    [Tags]    api
    ${res}=    Http Get    ${API}/specs/${URN}
    ${on_disk}=    Read File Text    ${SPEC_FILE}
    Should Be Equal    ${res}[body]    ${on_disk}
    Should Contain     ${res}[content_type]    yaml

The Reference Is Served For A Stored Spec
    [Tags]    api    doc
    ${res}=    Http Get    ${API}/specs/${URN}/doc
    Should Contain    ${res}[content_type]    text/html
    Should Contain    ${res}[body]           ISO 8583:1987
    Should Contain    ${res}[body]           Version 2.2.0

The Served Reference Is Public Unless Asked Otherwise
    [Documentation]    This endpoint carries no authentication of its own, and
    ...                `scope: private` marks what a spec's author decided not to
    ...                publish. The default has to be the safe one.
    [Tags]    api    doc    scope
    ${default}=     Http Get    ${API}/specs/${URN}/doc
    ${complete}=    Http Get    ${API}/specs/${URN}/doc?scope=complete
    Should Contain       ${default}[body]     are omitted from this variant
    Should Not Contain   ${complete}[body]    are omitted from this variant

    ${d}=    Count Data Elements    ${default}[body]
    ${c}=    Count Data Elements    ${complete}[body]
    Should Be True    ${d} < ${c}    msg=the served scopes withhold nothing: ${d} vs ${c}

The Served Reference Can Be Markdown
    [Tags]    api    doc
    ${res}=    Http Get    ${API}/specs/${URN}/doc?format=markdown
    Should Contain    ${res}[content_type]    text/markdown
    Should Contain    ${res}[body]           \# ISO 8583:1987
    Should Contain    ${res}[body]           Version 2.2.0

The Served Page Is As Navigable As The Rendered One
    [Documentation]    Same renderer, so the same guarantee: what a reader clicks
    ...                leads somewhere.
    [Tags]    api    doc    navigation
    ${res}=    Http Get    ${API}/specs/${URN}/doc?scope=complete
    ${ids}=    Every Internal Link Resolves    ${res}[body]
    Should Be True    ${ids} > 100
    Page Fetches Nothing External    ${res}[body]

An Unknown Scope Is Refused
    [Tags]    api    negative
    ${res}=    Http Get    ${API}/specs/${URN}/doc?scope=secret    expect_status=400
    Should Contain    ${res}[body]    public

An Unknown Format Is Refused
    [Tags]    api    negative
    ${res}=    Http Get    ${API}/specs/${URN}/doc?format=pdf    expect_status=400
    Should Contain    ${res}[body]    markdown

A Spec The Store Does Not Hold Is Not Found
    [Tags]    api    negative
    ${res}=    Http Get    ${API}/specs/no-such-spec/v1.0.0    expect_status=404
    Should Contain    ${res}[body]    no-such-spec:v1.0.0

    ${doc}=    Http Get    ${API}/specs/no-such-spec/v1.0.0/doc    expect_status=404
    Should Contain    ${doc}[body]    no-such-spec

A Version The Store Does Not Hold Is Not Found
    [Documentation]    The name exists and the version does not, which is the
    ...                case a fleet hits when a scenario names a spec that was
    ...                never rolled out.
    [Tags]    api    negative    versioning
    ${res}=    Http Get    ${API}/specs/iso8583-v87-ascii/v9.9.9    expect_status=404
    Should Contain    ${res}[body]    v9.9.9
