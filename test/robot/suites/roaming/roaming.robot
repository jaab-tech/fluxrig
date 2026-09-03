# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     Enriching an authorization with a mobile network signal.
...
...               The suite asserts degradation more than success. A signal that
...               arrives is the easy case; what decides whether this belongs in
...               an authorization path is what happens when it does not, and a
...               published deployment reports roughly one call in eight coming
...               back with no usable answer.
...
...               Each case is chosen by the card, because the number it is
...               enrolled under ends in the digits the operator simulator
...               switches on. A test therefore reads as the case it exercises.
Resource          roaming.resource
Library           fluxrigLibrary
Library           ISO8583Library
Library           String
Library           OperatingSystem
Suite Setup       Start Roaming Platform    ${CURDIR}
Suite Teardown    Stop Roaming Platform

*** Variables ***
# Enrolled cards. The trailing digits of the number each resolves to select the
# operator's behaviour; see scenarios/camara_sim.yaml.
${CARD_IN_UY}          4111111111111111
${CARD_IN_BR}          4222222222222222
${CARD_MULTI_COUNTRY}  4333333333333333
${CARD_EMPTY_LIST}     4444444444444444
${CARD_NO_COUNTRY}     4555555555555555
${CARD_SLOW}           4666666666666666
${CARD_OPERATOR_DOWN}  4777777777777777
# Not in the enrolment snapshot at all.
${CARD_NOT_ENROLLED}   4888888888888888
# The number the first card resolves to, which must never appear anywhere.
${MSISDN_IN_UY}        +59899000001

*** Test Cases ***
Handset In The Merchant's Country Is A Match
    [Documentation]    The ordinary case: card used in Uruguay, handset in Uruguay.
    ${reply}=    Authorize    ${CARD_IN_UY}    UY    000101
    Outcome Seen By Authorizer Should Be    ${reply}    RS00

Handset In Another Country Is A Mismatch
    [Documentation]    The signal the control exists to produce.
    ${reply}=    Authorize    ${CARD_IN_BR}    UY    000102
    Outcome Seen By Authorizer Should Be    ${reply}    RS01

One Country Code Covering Several Countries Matches Any Of Them
    [Documentation]    Mobile country code 340 covers BL, GF, GP, MF and MQ. The
    ...                comparison is membership, so a merchant in any of them is
    ...                a match. Reading only the first entry would report a
    ...                mismatch for four countries out of five, which on a fraud
    ...                control is friction applied to good transactions.
    ${reply}=    Authorize    ${CARD_MULTI_COUNTRY}    MQ    000103
    Outcome Seen By Authorizer Should Be    ${reply}    RS00

An Empty Country List Is Its Own Outcome, Not A Mismatch
    [Documentation]    An empty list means the code maps to no country, as 901
    ...                does for the global networks. That is an absent answer,
    ...                and reporting it as a mismatch would invent a signal.
    ${reply}=    Authorize    ${CARD_EMPTY_LIST}    UY    000104
    Outcome Seen By Authorizer Should Be    ${reply}    RS13

A Reply Without Country Fields Has No Answer
    [Documentation]    Only `roaming` is required by the contract. Both country
    ...                fields may be absent and the pipeline must not assume them.
    ${reply}=    Authorize    ${CARD_NO_COUNTRY}    UY    000105
    Outcome Seen By Authorizer Should Be    ${reply}    RS12

An Operator Slower Than The Time Budget Does Not Hold Up The Payment
    [Documentation]    The case the whole design is for. The operator answers,
    ...                but after the time budget, so the authorization proceeds
    ...                carrying `unknown` rather than waiting.
    # Measured against a fast card in the same run, not against a fixed number.
    # An absolute bound would encode this machine's speed and fail on a slower
    # one for reasons that have nothing to do with the time budget.
    ${fast}=    Time One Authorization    ${CARD_IN_UY}    000106
    ${slow}=    Time One Authorization    ${CARD_SLOW}    000107

    # Timing first, and separately, because the two assertions fail for
    # different reasons and a reader of a red run should not have to work out
    # which. The outcome alone does not prove the payment was not held up: a
    # pipeline that waited for the operator produces the same value, late.
    #
    # The operator sleeps 400ms and the time budget is 250ms, so a slow card costs
    # about that much more than a fast one when the budget holds, and about the
    # whole sleep when it does not. The bound sits between the two: comfortably
    # above 250ms so a loaded machine does not fail for spending what it was
    # allowed, and below 400ms so a pipeline that waited for the operator is
    # caught.
    ${extra}=    Evaluate    ${slow} - ${fast}
    Should Be True    ${extra} < 0.35
    ...    msg=the slow card cost ${extra}s more than a fast one, which is nearer the operator's delay than the time budget: the time budget did not hold

    ${reply}=    Authorize    ${CARD_SLOW}    UY    000108
    Outcome Seen By Authorizer Should Be    ${reply}    RS12

An Operator Error Is Named As One
    ${reply}=    Authorize    ${CARD_OPERATOR_DOWN}    UY    000107
    Outcome Seen By Authorizer Should Be    ${reply}    RS11

A Card Absent From The Enrolment Snapshot Still Authorizes
    [Documentation]    Coverage is partial by design: not every card has a usable
    ...                number on file. That is a declared outcome, not an error.
    ${reply}=    Authorize    ${CARD_NOT_ENROLLED}    UY    000109
    No Outcome Should Have Reached The Authorizer    ${reply}

Every Authorization Is Approved Regardless Of The Signal
    [Documentation]    Nothing here decides an outcome. The enrichment adds a
    ...                field; acting on it is the institution's decision, and a
    ...                suite that let the enrichment decline would be testing a
    ...                policy this scenario does not hold.
    FOR    ${card}    IN    ${CARD_IN_UY}    ${CARD_IN_BR}    ${CARD_OPERATOR_DOWN}
        ${reply}=    Authorize    ${card}    UY    000109
        ${ascii}=    Reply As Ascii    ${reply}
        Should Contain    ${ascii}    00    msg=DE 39 must approve
    END

The Reply To The Scheme Carries No Private Field
    [Documentation]    The reply is relayed to the scheme exactly as the
    ...                authorizer wrote it, with no re-encode. Anything left in a
    ...                private field would travel to a counterparty whose dialect
    ...                does not declare it, so its absence is a property worth
    ...                asserting rather than assuming.
    ${reply}=    Authorize    ${CARD_IN_UY}    UY    000110
    ${bitmap}=    Get Substring    ${reply}    8    24
    Private Field 48 Should Be Absent    ${bitmap}

The Phone Number Reaches Neither The Wire Nor The Logs
    [Documentation]    The claim that fails silently when nobody asserts it.
    ...
    ...                The number is read from the enrolment snapshot, sent to
    ...                the operator and dropped. Nothing downstream needs it, so
    ...                nothing downstream would complain if it leaked: it would
    ...                simply sit in a reply or a log file until someone went
    ...                looking. The gear runs at WARN for this reason, and that
    ...                is a setting one careless edit undoes.
    ${reply}=    Authorize    ${CARD_IN_UY}    UY    000111
    ${ascii}=    Reply As Ascii    ${reply}
    Should Not Contain    ${ascii}    ${MSISDN_IN_UY}
    ...    msg=the enrolled number came back on the wire
    FOR    ${r}    IN    fluxrig    telco
        ${found}=    Grep File    ${WORK_DIR}/${r}/logs/process_stdout.log    ${MSISDN_IN_UY}
        Should Be Empty    ${found}
        ...    msg=rack-${r} logged the enrolled number
    END
    ${found}=    Grep File    ${WORK_DIR}/authorizer.log    ${MSISDN_IN_UY}
    Should Be Empty    ${found}    msg=the authorizer logged the enrolled number

A Reversal Reusing A Trace Number Does Not Collide With Its Authorization
    [Documentation]    The correlation key carries the message class for exactly
    ...                this case. A reversal reuses the trace number of the
    ...                authorization it reverses, which is normal in several
    ...                dialects, so `(DE 41, DE 11)` alone is the same key for
    ...                both and the second parks over the first.
    ...
    ...                Both are sent before either reply is read, so the two
    ...                contexts are parked at the same time by construction
    ...                rather than by timing luck. Each connection must then get
    ...                back its own message class: 0210 for the authorization,
    ...                0410 for the reversal.
    ${location}=    Acceptor Location    SHOP    UY
    ${auth}=    Build ISO Message    0100
    ...    f2=${CARD_IN_UY}    f3=000000    f4=000000010000    f7=0101120000
    ...    f11=000501    f18=5411    f22=051    f41=TERM0001    f43=${location}
    ${reversal}=    Build ISO Message    0400
    ...    f2=${CARD_IN_UY}    f3=000000    f4=000000010000    f7=0101120000
    ...    f11=000501    f18=5411    f22=051    f41=TERM0001    f43=${location}

    ${replies}=    Send Iso Messages Together    ${{ [$auth, $reversal] }}
    ...    target=127.0.0.1:${SCHEME_PORT}    header_len=2

    ${auth_mti}=    Reply Mti    ${replies}[0]
    ${rev_mti}=     Reply Mti    ${replies}[1]
    Should Be Equal    ${auth_mti}    0110
    ...    msg=the authorization's connection got ${auth_mti}: its context was overwritten by the reversal
    Should Be Equal    ${rev_mti}    0410
    ...    msg=the reversal's connection got ${rev_mti}: it received the authorization's reply


An Expired Correlation Entry Loses The Reply, Loudly
    [Documentation]    The TTL is not only a memory bound: it is a ceiling on how
    ...                long the authorizer may take. The connection the request
    ...                arrived on is parked in the correlation entry, so once that
    ...                entry is swept there is nowhere to send the answer. The
    ...                reply still crosses the pipeline and is dropped at the last
    ...                gear.
    ...
    ...                `on_missing: forward` does not save it. Forwarding a reply
    ...                whose routing information is gone forwards it into a dead
    ...                end, which is worth knowing before someone sets a TTL below
    ...                their authorizer's worst case.
    ...
    ...                What this asserts is that the loss is visible. A dropped
    ...                authorization that logged nothing would be found by the
    ...                scheme's timeout graph and nowhere else. Runs last: it
    ...                leaves the authorizer slow.
    Restart Authorizer With Delay    4s
    ${status}    ${err}=    Run Keyword And Ignore Error
    ...    Authorize    ${CARD_IN_UY}    UY    000601
    Should Be Equal    ${status}    FAIL
    ...    msg=the reply survived a swept correlation entry, which the design does not promise
    ${found}=    Grep File    ${WORK_DIR}/fluxrig/logs/process_stdout.log    not found
    Should Not Be Empty    ${found}
    ...    msg=the switch dropped a reply and logged nothing about it



*** Keywords ***
Outcome Seen By Authorizer Should Be
    [Documentation]    Reads DE 42 from the reply, where the simulated authorizer
    ...                echoes the outcome it received in the private field.
    [Arguments]    ${reply_hex}    ${expected}
    ${ascii}=    Reply As Ascii    ${reply_hex}
    Should Contain    ${ascii}    ${expected}
    ...    msg=authorizer should have received '${expected}'; reply was ${ascii}

Time One Authorization
    [Documentation]    Returns how long one authorization took, end to end.
    [Arguments]    ${pan}    ${stan}
    ${start}=    Evaluate    time.time()    modules=time
    Authorize    ${pan}    UY    ${stan}
    ${elapsed}=    Evaluate    time.time() - ${start}    modules=time
    RETURN    ${elapsed}

No Outcome Should Have Reached The Authorizer
    [Documentation]    The authorizer reports back whatever arrived in the
    ...                private field. Nothing arriving means nothing to report,
    ...                so the absence of a code is the assertion: a card with no
    ...                number on file was never asked about.
    [Arguments]    ${reply_hex}
    ${ascii}=    Reply As Ascii    ${reply_hex}
    Should Not Match Regexp    ${ascii}    RS[0-9][0-9]
    ...    msg=an outcome reached the authorizer for a card that is not enrolled: ${ascii}

Reply Mti
    [Documentation]    The first four characters of a reply, which are its MTI.
    [Arguments]    ${reply_hex}
    ${ascii}=    Reply As Ascii    ${reply_hex}
    ${mti}=    Get Substring    ${ascii}    0    4
    RETURN    ${mti}

Reply As Ascii
    [Arguments]    ${reply_hex}
    ${bytes}=    Convert To Bytes    ${reply_hex}    hex
    ${text}=    Convert To String    ${bytes}
    RETURN    ${text}

Private Field 48 Should Be Absent
    [Documentation]    DE 48 is bit 48, the last bit of the sixth bitmap byte.
    [Arguments]    ${bitmap_hex}
    ${byte6}=    Get Substring    ${bitmap_hex}    10    12
    ${value}=    Convert To Integer    ${byte6}    16
    ${bit48}=    Evaluate    ${value} & 1
    Should Be Equal As Integers    ${bit48}    0
    ...    msg=DE 48 is set in the reply to the scheme; the outcome leaked
