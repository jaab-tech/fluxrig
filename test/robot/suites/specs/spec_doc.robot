# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     The protocol reference the CLI renders from a spec, in both
...               formats and both scopes.
...
...               What matters here is not that a page is produced. It is that
...               the public variant withholds what the spec marked private, that
...               the page works on a machine with no network, and that a reader
...               who clicks something arrives somewhere -- a reference is
...               navigated, not read front to back.
Library           SpecLibrary
Library           OperatingSystem
Library           String
Library           fluxrigLibrary
Suite Setup       Render Every Variant

*** Keywords ***
Render Every Variant
    [Documentation]    Renders the four combinations once; each test then asks
    ...                one question of them.
    ${work}=    Setup Workspace    ${CURDIR}
    Set Suite Variable    ${OUT}    ${work}/doc
    Create Directory    ${OUT}
    ${spec}=    Repository Path    examples    specs    iso8583-v87-ascii.yaml
    Set Suite Variable    ${SPEC}    ${spec}
    FOR    ${scope}    IN    public    complete
        FOR    ${format}    IN    markdown    html
            Run Fluxrig    spec    doc    ${spec}
            ...    --scope    ${scope}    --format    ${format}
            ...    --out    ${OUT}/${scope}.${format}
        END
    END
    Set Suite Variable    ${PUBLIC_HTML}      ${{ open(r"${OUT}/public.html", encoding="utf-8").read() }}
    Set Suite Variable    ${COMPLETE_HTML}    ${{ open(r"${OUT}/complete.html", encoding="utf-8").read() }}
    Set Suite Variable    ${PUBLIC_MD}        ${{ open(r"${OUT}/public.markdown", encoding="utf-8").read() }}
    Set Suite Variable    ${COMPLETE_MD}      ${{ open(r"${OUT}/complete.markdown", encoding="utf-8").read() }}

*** Test Cases ***
The Public Variant Withholds What The Spec Marked Private
    [Documentation]    This is the only variant eligible for publication, so the
    ...                withholding is the whole point of having two.
    [Tags]    doc    scope
    ${public}=      Count Data Elements    ${PUBLIC_HTML}
    ${complete}=    Count Data Elements    ${COMPLETE_HTML}
    Should Be True    ${public} < ${complete}
    ...    msg=public documents ${public} elements and complete ${complete}; the scopes are not separating anything

The Public Variant Says How Much It Withheld
    [Documentation]    A reader who cannot see something should know it exists
    ...                and is not theirs, rather than reading a shorter document
    ...                and believing it complete.
    [Tags]    doc    scope
    Should Contain       ${PUBLIC_HTML}      are omitted from this variant
    Should Not Contain   ${COMPLETE_HTML}    are omitted from this variant

The Reference Names The Contract It Was Rendered From
    [Documentation]    A reference outlives the deployment it described, and
    ...                paper carries no URL: without the version on the page a
    ...                reader cannot tell which protocol they are holding.
    [Tags]    doc    versioning
    Should Contain    ${PUBLIC_HTML}      Version 2.2.0
    Should Contain    ${COMPLETE_HTML}    Version 2.2.0
    Should Contain    ${PUBLIC_MD}        2.2.0

Every Link In The Page Leads Somewhere
    [Documentation]    A reference is navigated. A link into a section that does
    ...                not exist is a dead end found instead of the answer.
    [Tags]    doc    navigation
    ${ids}=    Every Internal Link Resolves    ${PUBLIC_HTML}
    Should Be True    ${ids} > 100    msg=only ${ids} anchors; the page is not the reference
    Every Internal Link Resolves    ${COMPLETE_HTML}

The Page Fetches Nothing From The Network
    [Documentation]    A protocol reference gets mailed and opened offline. A
    ...                page that pulls a stylesheet renders as unstyled text on
    ...                the machine that matters.
    [Tags]    doc    offline
    Page Fetches Nothing External    ${PUBLIC_HTML}
    Page Fetches Nothing External    ${COMPLETE_HTML}

The Page Carries The fluxrig Mark
    [Tags]    doc
    Should Contain    ${PUBLIC_HTML}    <svg
    Should Contain    ${PUBLIC_HTML}    class="logo"

Both Formats Document The Same Protocol
    [Documentation]    Two renderers over one model. If they disagree on how many
    ...                elements exist, one of them is deriving its own.
    [Tags]    doc
    ${html}=    Count Data Elements    ${PUBLIC_HTML}
    ${rows}=    Get Lines Matching Regexp    ${PUBLIC_MD}    ^#{2,3} DE \\d+.*    partial_match=${False}
    ${md}=      Get Line Count    ${rows}
    Should Be Equal As Integers    ${html}    ${md}
    ...    msg=the HTML documents ${html} elements and the markdown ${md}

An Unknown Scope Is Refused
    [Tags]    doc    negative
    ${res}=    Run Fluxrig    spec    doc    ${SPEC}    --scope    secret    expect_failure=${True}
    Should Contain    ${res}[output]    public

An Unknown Format Is Refused
    [Tags]    doc    negative
    ${res}=    Run Fluxrig    spec    doc    ${SPEC}    --format    pdf    expect_failure=${True}
    Should Contain    ${res}[output]    html

A Spec That Does Not Load Produces No Reference
    [Documentation]    Rendering an unresolvable spec would produce a document
    ...                describing links that go nowhere, which is worse than an
    ...                error naming them.
    [Tags]    doc    negative
    ${body}=    Catenate    SEPARATOR=\n
    ...    spec:
    ...    ${SPACE}${SPACE}id: dangling
    ...    ${SPACE}${SPACE}version: "1.0.0"
    ...    ${SPACE}${SPACE}protocol: iso8583
    ...    ${SPACE}${SPACE}wire:
    ...    ${SPACE}${SPACE}${SPACE}${SPACE}format: moov
    ...    ${SPACE}${SPACE}${SPACE}${SPACE}source: "moov:spec87ascii"
    ...    ${SPACE}${SPACE}fields:
    ...    ${SPACE}${SPACE}${SPACE}${SPACE}2:
    ...    ${SPACE}${SPACE}${SPACE}${SPACE}${SPACE}${SPACE}alias: pan
    ...    ${SPACE}${SPACE}${SPACE}${SPACE}${SPACE}${SPACE}values_ref: a_value_set_that_was_never_declared
    ...    ${EMPTY}
    ${path}=    Write Spec    ${OUT}/../fixtures/dangling.yaml    ${body}
    ${res}=     Run Fluxrig    spec    doc    ${path}    expect_failure=${True}
    Should Contain    ${res}[output]    a_value_set_that_was_never_declared
