# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     A spec states a contract, and these are the rules that make the
...               contract referable: it declares who it is and which version it
...               is, the store files it under what it declared, and the same
...               name and version can never mean two different documents.
...
...               These rules were unenforced for most of the project's life. The
...               schema asked for a version, nothing read it, and a spec with no
...               version at all loaded clean.
Library           SpecLibrary
Library           OperatingSystem
Library           fluxrigLibrary
Suite Setup       Prepare Store

*** Keywords ***
Prepare Store
    [Documentation]    Everything this suite writes goes to a temporary
    ...                workspace. A suite that leaves files in the checkout makes
    ...                the next run depend on the last one.
    ${work}=    Setup Workspace    ${CURDIR}
    Set Suite Variable    ${STORE}       ${work}/store
    Set Suite Variable    ${FIXTURES}    ${work}/fixtures
    Create Directory    ${STORE}

Spec With Header
    [Documentation]    The smallest spec the loader accepts, with the header left
    ...                to the caller so a test states exactly what it changes.
    [Arguments]    ${header}
    ${body}=    Catenate    SEPARATOR=\n
    ...    spec:
    ...    ${header}
    ...    ${SPACE}${SPACE}protocol: iso8583
    ...    ${SPACE}${SPACE}wire:
    ...    ${SPACE}${SPACE}${SPACE}${SPACE}format: moov
    ...    ${SPACE}${SPACE}${SPACE}${SPACE}source: "moov:spec87ascii"
    ...    ${SPACE}${SPACE}fields:
    ...    ${SPACE}${SPACE}${SPACE}${SPACE}2:
    ...    ${SPACE}${SPACE}${SPACE}${SPACE}${SPACE}${SPACE}alias: pan
    ...    ${EMPTY}
    RETURN    ${body}

*** Test Cases ***
The Reference Spec Satisfies The Rules It Documents
    [Documentation]    The spec the docs walk through has to pass first, or the
    ...                rule is one nobody in this repository has adopted.
    [Tags]    contract
    ${spec}=    Repository Path    examples    specs    iso8583-v87-ascii.yaml
    ${res}=     Run Fluxrig    spec    import    ${spec}    --store-dir    ${STORE}
    Should Contain    ${res}[output]    iso8583-v87-ascii:v2.2.0

A Spec Without A Version Is Refused
    [Documentation]    The gap this suite exists for. A version states which
    ...                contract; without it a trace can say which bytes ran and
    ...                nothing a human can act on.
    [Tags]    contract    negative
    ${body}=    Spec With Header    ${SPACE}${SPACE}id: no-version\n${SPACE}${SPACE}name: No Version
    ${path}=    Write Spec    ${FIXTURES}/no_version.yaml    ${body}
    ${res}=     Run Fluxrig    spec    doc    ${path}    expect_failure=${True}
    Should Contain    ${res}[output]    spec.version is required

A Version That Is Not A Version Is Refused
    [Documentation]    "latest" is a tag, not a contract: it names whatever is
    ...                current, which is the one thing a version must not do.
    [Tags]    contract    negative
    ${body}=    Spec With Header    ${SPACE}${SPACE}id: bad-version\n${SPACE}${SPACE}version: "latest"
    ${path}=    Write Spec    ${FIXTURES}/bad_version.yaml    ${body}
    ${res}=     Run Fluxrig    spec    doc    ${path}    expect_failure=${True}
    Should Contain    ${res}[output]    not a semantic version

A Spec Without An Identity Is Refused
    [Documentation]    A version alone does not name a contract. A Rack running
    ...                two codecs sees two specs both calling themselves 1.0.0.
    [Tags]    contract    negative
    ${body}=    Spec With Header    ${SPACE}${SPACE}version: "1.0.0"
    ${path}=    Write Spec    ${FIXTURES}/no_id.yaml    ${body}
    ${res}=     Run Fluxrig    spec    doc    ${path}    expect_failure=${True}
    Should Contain    ${res}[output]    spec.id is required

The Refusal Names The Spec It Is About
    [Documentation]    A Rack loads several. An error saying only that a version
    ...                is required leaves the operator to guess which one.
    [Tags]    contract
    ${body}=    Spec With Header    ${SPACE}${SPACE}id: acme-auth\n${SPACE}${SPACE}name: Acme
    ${path}=    Write Spec    ${FIXTURES}/named.yaml    ${body}
    ${res}=     Run Fluxrig    spec    doc    ${path}    expect_failure=${True}
    Should Contain    ${res}[output]    acme-auth

The Store Files A Spec Under What It Declares
    [Documentation]    Not under a default, and not under its human title. The
    ...                CAS read neither field for most of the project's life, so
    ...                a spec declaring 2.2.0 was stored as v0.1.0.
    [Tags]    contract    store
    ${body}=    Spec With Header    ${SPACE}${SPACE}id: filed-right\n${SPACE}${SPACE}name: A Title, With (Punctuation)\n${SPACE}${SPACE}version: "3.1.4"
    ${path}=    Write Spec    ${FIXTURES}/filed.yaml    ${body}
    ${res}=     Run Fluxrig    spec    import    ${path}    --store-dir    ${STORE}
    Should Contain       ${res}[output]    filed-right:v3.1.4
    Should Not Contain   ${res}[output]    v0.1.0
    Should Not Contain   ${res}[output]    Punctuation

The Same Bytes Twice Are One Artefact
    [Documentation]    Not two versions of themselves. Every import used to
    ...                auto-increment, so one file became v0.1.0 and v0.2.0 with
    ...                identical hashes.
    [Tags]    contract    store
    ${res}=    Run Fluxrig    spec    import    ${FIXTURES}/filed.yaml    --store-dir    ${STORE}
    Should Contain       ${res}[output]    filed-right:v3.1.4
    Should Not Contain   ${res}[output]    v3.1.5

One Version Cannot Mean Two Documents
    [Documentation]    The immutability check existed and could never fire,
    ...                because no two imports ever landed on the same tag.
    [Tags]    contract    store    negative
    ${body}=    Spec With Header    ${SPACE}${SPACE}id: filed-right\n${SPACE}${SPACE}name: Different Content\n${SPACE}${SPACE}version: "3.1.4"
    ${path}=    Write Spec    ${FIXTURES}/filed_modified.yaml    ${body}
    ${res}=     Run Fluxrig    spec    import    ${path}    --store-dir    ${STORE}    expect_failure=${True}
    Should Contain    ${res}[output]    already exists

The Store Lists What It Holds
    [Tags]    contract    store
    ${res}=    Run Fluxrig    spec    list    --store-dir    ${STORE}
    Should Contain    ${res}[output]    filed-right
    Should Contain    ${res}[output]    iso8583-v87-ascii
