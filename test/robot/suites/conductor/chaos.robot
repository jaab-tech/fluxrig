*** Settings ***
Documentation     Conductor communication-failure / chaos validation.
...               The two scheme uplinks run through Toxiproxy. While mixed load
...               flows, one scheme's link is cut and then restored. Asserts the
...               switch degrades gracefully (timeout/no-destination -> decline,
...               never a dropped terminal, never a cross-wired reply), that the
...               fault stays isolated to the affected BIN, and that the path
...               recovers once the link is restored.
Resource          conductor.resource
Library           ConductorLibrary
Library           ToxiProxyLibrary
Library           Process
Suite Setup       Setup Chaos Suite
Suite Teardown    Teardown Chaos Suite

*** Variables ***
${SCHEME_A_PORT}       10001
${SCHEME_B_PORT}       10002
${PROXY_A_LISTEN}      127.0.0.1:20001
${PROXY_B_LISTEN}      127.0.0.1:20002
# Terminal-facing proxy: POS terminals reach the switch ingress (:8583) through
# this listener, so a suite can blip the terminal link, not just the uplinks.
${TERMINAL_PROXY}      127.0.0.1:28583
${TERMINAL_PROXY_PT}   28583

*** Keywords ***
Setup Chaos Suite
    Start Switch Platform    ${CURDIR}
    # Both schemes approve when healthy, so any decline signals a link fault.
    Start Scheme Host    alias=scheme_a    port=${SCHEME_A_PORT}    spec=${SPEC}    de39=00
    Start Scheme Host    alias=scheme_b    port=${SCHEME_B_PORT}    spec=${SPEC}    de39=00
    Wait For Port    port=${SCHEME_A_PORT}    timeout=10
    Wait For Port    port=${SCHEME_B_PORT}    timeout=10
    # Toxiproxy in front of each scheme.
    Start Process    toxiproxy-server    stdout=${WORK_DIR}/toxiproxy.log    stderr=STDOUT    alias=toxiproxy
    Sleep    1s
    Connect To Toxiproxy    localhost:8474
    Create Proxy    scheme_a    127.0.0.1:${SCHEME_A_PORT}    ${PROXY_A_LISTEN}
    Create Proxy    scheme_b    127.0.0.1:${SCHEME_B_PORT}    ${PROXY_B_LISTEN}
    Deploy Switch Scenario    ${CURDIR}/scenarios/switch_proxied.yaml
    # Terminal-facing proxy in front of the (now-listening) switch ingress.
    Create Proxy    terminal_ingress    127.0.0.1:${SWITCH_PORT}    ${TERMINAL_PROXY}
    Wait For Port    port=${TERMINAL_PROXY_PT}    timeout=10

Teardown Chaos Suite
    Run Keyword And Ignore Error    Delete Proxy    scheme_a
    Run Keyword And Ignore Error    Delete Proxy    scheme_b
    Run Keyword And Ignore Error    Delete Proxy    terminal_ingress
    Run Keyword And Ignore Error    Terminate Process    toxiproxy
    Stop Switch Platform

*** Test Cases ***
Cutting One Scheme Degrades Only Its BIN And Recovers
    [Documentation]    While BIN4 (scheme A) and BIN5 (scheme B) both flow, cut
    ...                scheme A mid-load then restore it. BIN4 must see approvals
    ...                (baseline + recovery) AND declines (during the cut), with
    ...                zero cross-wiring and no dropped terminal. BIN5 must stay
    ...                perfectly healthy throughout (fault isolation).
    [Tags]    conductor    chaos
    # Background load: one terminal per scheme, ~35s of Poisson traffic.
    Start Auth Terminal    alias=term_a    target=127.0.0.1:${SWITCH_PORT}    spec=${SPEC}
    ...    report_file=${WORK_DIR}/chaos_bin4.json
    ...    pan=4    expect_mti=0210    accept_de39=00,91,05    conns=2    count=350    rate=10    stan_base=100000    timeout=15s
    Start Auth Terminal    alias=term_b    target=127.0.0.1:${SWITCH_PORT}    spec=${SPEC}
    ...    report_file=${WORK_DIR}/chaos_bin5.json
    ...    pan=5    expect_mti=0210    expect_de39=00    conns=2    count=350    rate=10    stan_base=400000    timeout=15s

    Sleep    7s    reason=Baseline: both schemes healthy
    Log    >>> CUT scheme A link
    Cut Link    scheme_a
    Sleep    14s    reason=Chaos: BIN4 degrades, BIN5 unaffected
    Log    >>> RESTORE scheme A link
    Restore Link    scheme_a
    Sleep    8s    reason=Recovery: BIN4 returns to approvals

    ${a}=    Wait Auth Terminal    term_a    timeout=120
    ${b}=    Wait Auth Terminal    term_b    timeout=120
    Log    BIN4 (scheme A, chaos): ${a}
    Log    BIN5 (scheme B, healthy): ${b}

    # Hard invariant everywhere: no reply ever went to the wrong terminal.
    Assert No Cross Wiring    ${a}
    Assert No Cross Wiring    ${b}

    # BIN5 is isolated from the fault: every reply approved, none failed.
    Assert All Ok    ${b}
    Assert De39 Seen    ${b}    00

    # BIN4 degraded gracefully and recovered: approvals seen (baseline+recovery),
    # declines seen (during the cut), and every txn still got a well-formed reply.
    Assert All Ok    ${a}
    Assert De39 Seen    ${a}    00
    Assert Declines Present    ${a}    approve_de39=00

Terminal Ingress Blip Is Survived With No Cross-Wiring
    [Documentation]    Chaos on the TERMINAL side: several POS terminals reach
    ...                the switch through a Toxiproxy in front of the ingress.
    ...                Their link is cut mid-load and restored; the terminals
    ...                reconnect. The switch must never mis-deliver a reply to a
    ...                reconnected terminal (cross_wired==0), every reply that
    ...                arrives must be well-formed (failed==0), and the large
    ...                majority of transactions must still complete once the link
    ...                is back. In-flight txns lost to the reset are counted as
    ...                dropped, not failed.
    [Tags]    conductor    chaos
    Start Auth Terminal    alias=term_x    target=${TERMINAL_PROXY}    spec=${SPEC}
    ...    mix=4:00,5:00    expect_mti=0210    accept_de39=00    reconnect=${TRUE}
    ...    conns=4    count=200    rate=10    stan_base=700000    timeout=15s
    ...    report_file=${WORK_DIR}/chaos_terminal.json

    Sleep    7s    reason=Baseline: terminals healthy
    Log    >>> CUT terminal ingress link
    Cut Link    terminal_ingress
    Sleep    10s    reason=Blip: all terminals lose their link
    Log    >>> RESTORE terminal ingress link
    Restore Link    terminal_ingress
    Sleep    10s    reason=Recovery: terminals reconnect and resume

    ${x}=    Wait Auth Terminal    term_x    timeout=120
    Log    Terminal-side chaos: ${x}
    Assert Survived Reconnects    ${x}    min_ok_ratio=0.7
