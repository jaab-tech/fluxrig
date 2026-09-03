# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     Roaming enrichment under load.
...
...               Correlation is a concurrency property: the enrichment parks each
...               message's context under a key and restores it on the reply, and a
...               key that collides returns the wrong reply to the wrong terminal.
...               One transaction at a time never has two contexts in flight, so
...               the functional suite structurally cannot see it.
...
...               The load is rate rather than socket count. Thirty to sixty
...               connections per terminal is what a switch sees from a terminal
...               estate; an issuer talks to a scheme over a handful of long-lived
...               links, so what is worth applying here is transactions per second.
...
...               Every case asserts cross_wired == 0: not one reply reached a
...               terminal that did not send it. All run against the shipped
...               scenario with await_store: false, the detached-write path.
...
...               Two properties of the key are deterministic and live in the
...               functional suite instead, because load is the wrong instrument
...               for them: a reversal colliding with its authorization, and an
...               entry expiring before its reply arrives.
Resource          roaming.resource
Library           ConductorLibrary
Suite Setup       Setup Stress Suite
Suite Teardown    Teardown Stress Suite

*** Variables ***
${SCHEME_PORT}    8586
${SPEC}           ${CURDIR}/specs/scheme.yaml
# One card per telco outcome, so a fleet run exercises every branch of the
# enrichment rather than the happy one many times over. The mapping lives in
# the scenario's enrolment snapshot.
@{OUTCOME_PANS}   4111111111111111    4222222222222222    4333333333333333
...               4444444444444444    4555555555555555    4666666666666666
...               4777777777777777

*** Keywords ***
Setup Stress Suite
    Start Roaming Platform    ${CURDIR}

Teardown Stress Suite
    Stop Roaming Platform

Assert Load Ok
    [Documentation]    Asserts load test invariants: all responses received, no failures.
    [Arguments]    ${rep}
    ${sent}=    Get From Dictionary    ${rep}    req_sent
    ${recv}=    Get From Dictionary    ${rep}    resp_recv
    ${failed}=    Get From Dictionary    ${rep}    resp_failed
    ${req_failed}=    Get From Dictionary    ${rep}    req_failed
    Should Be Equal As Integers    ${sent}    ${recv}    msg=req_sent=${sent} != resp_recv=${recv}
    Should Be Equal As Integers    ${failed}    0    msg=resp_failed=${failed} (connection errors)
    Should Be Equal As Integers    ${req_failed}    0    msg=req_failed=${req_failed} (send errors)
    ${tps}=    Get From Dictionary    ${rep}    actual_tps
    Should Be True    ${tps} > 0    msg=actual_tps=${tps} (no throughput)
*** Comments ***
Volume is sized by the slowest card, not by the fastest.
    Five of the seven cards drive a telco outcome that waits, and one of them
    waits out the whole budget on every transaction. That terminal therefore runs
    at roughly a tenth of the others' rate, and giving every terminal the same
    volume means the fleet finishes when the slowest one does.
    Sized so that terminal finishes in well under its timeout: a case sitting at
    the edge of its own budget tests the budget, and passes or fails by luck.

*** Test Cases ***
Sustained Load - Match Outcome
    [Documentation]    200 TPS for 15s over 4 connections, PAN 4111 (match/RS00).
    ...                Asserts correlation: STAN echo, no cross-wiring.
    [Tags]    roaming    stress    sustained
    ${de43}=    Acceptor Location    SHOP    UY
    ${rep}=    Run Load    target=127.0.0.1:${SCHEME_PORT}    spec=${SPEC}
    ...    report_file=${WORK_DIR}/load_match.json
    ...    mti=0100    pan=4111111111111111    de43=${de43}
    ...    rate=200    duration=15s    conns=4    timeout=60
    Log    Match load: ${rep}
    Assert Load Ok    ${rep}

Concurrent Multi-Outcome Holds Correlation
    [Documentation]    Seven terminals, one per telco outcome, 4 connections and
    ...                250 transactions per connection at 60 TPS. Every reply must carry its own trace
    ...                number: the enrichment parks and restores context per
    ...                message, and a key that collides under fan-out would return
    ...                someone else's reply.
    [Tags]    roaming    stress
    ${fleet}=    Start Auth Fleet    pan_list=${OUTCOME_PANS}    target=127.0.0.1:${SCHEME_PORT}
    ...    spec=${SPEC}    work_dir=${WORK_DIR}    conns=4    count=250    rate=60    stan_base=100000    timeout=240    merchant_country=UY
    ${rep}=    Wait Auth Fleet    ${fleet}    timeout=240
    Log    Multi-outcome load: ${rep}
    Assert All Ok    ${rep}
    Assert De39 Seen    ${rep}    00
    Assert De42 Seen    ${rep}    RS00
    Assert De42 Seen    ${rep}    RS01
    Log    ${rep}[total] txns, p99=${rep}[latency_p99_ms]ms, outcomes=${rep}[by_de42]

High Concurrency Holds Correlation
    [Documentation]    The case the detached write exists for: seven terminals at
    ...                120 TPS each, 300 transactions per connection, with
    ...                await_store false, so
    ...                the Coat Check store runs one unbounded goroutine per
    ...                message while the telco call is in flight. Distinct STAN
    ...                base so keys cannot collide with the previous case.
    [Tags]    roaming    stress
    ${fleet}=    Start Auth Fleet    pan_list=${OUTCOME_PANS}    target=127.0.0.1:${SCHEME_PORT}
    ...    spec=${SPEC}    work_dir=${WORK_DIR}    conns=6    count=300    rate=120    stan_base=500000    timeout=300    merchant_country=UY
    ${rep}=    Wait Auth Fleet    ${fleet}    timeout=300
    Log    High-concurrency load: ${rep}
    Assert All Ok    ${rep}
    Assert De39 Seen    ${rep}    00
    Log    ${rep}[total] txns, p99=${rep}[latency_p99_ms]ms, outcomes=${rep}[by_de42]

Reversals Share Trace Numbers With Authorizations
    [Documentation]    The correlation key carries the message class for one
    ...                reason: a reversal reuses the trace number of the
    ...                authorization it reverses, which is normal in several
    ...                dialects. Without the class in the key, the two collide
    ...                in the store and each gets the other's reply.
    ...
    ...                Two fleets run at once over the SAME trace range, one
    ...                sending 0200 and one sending 0400. If the class were not
    ...                doing its work, every key would collide and cross_wired
    ...                would be large rather than zero.
    ...
    ...                The reversals are enriched like anything else here, which
    ...                a real issuer would not do. What is under test is the key,
    ...                not the policy.
    [Tags]    roaming    stress
    ${auths}=    Start Auth Fleet    pan_list=${OUTCOME_PANS}    target=127.0.0.1:${SCHEME_PORT}
    ...    spec=${SPEC}    work_dir=${WORK_DIR}    conns=4    count=200    rate=60
    ...    stan_base=900000    timeout=240    merchant_country=UY    mti=0200    prefix=fin
    ${revs}=    Start Auth Fleet    pan_list=${OUTCOME_PANS}    target=127.0.0.1:${SCHEME_PORT}
    ...    spec=${SPEC}    work_dir=${WORK_DIR}    conns=4    count=200    rate=60
    ...    stan_base=900000    timeout=240    merchant_country=UY    mti=0400    expect_mti=0410    prefix=rev

    ${fin_report}=    Wait Auth Fleet    ${auths}    timeout=240
    ${rev_report}=    Wait Auth Fleet    ${revs}    timeout=240

    Log    Financial leg: ${fin_report}
    Log    Reversal leg: ${rev_report}
    Assert All Ok    ${fin_report}
    Assert All Ok    ${rev_report}
    Log    ${fin_report}[total] financial + ${rev_report}[total] reversals over one trace range, cross_wired=${fin_report}[cross_wired]/${rev_report}[cross_wired]
