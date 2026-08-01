*** Settings ***
Documentation     Conductor stress / load validation.
...               Drives the payment switch with many concurrent, multi-scheme
...               terminals and asserts the correlation guarantee holds at load:
...               every reply returns to the terminal that sent it (STAN echo),
...               no cross-wiring, and both approve and decline paths execute.
Resource          conductor.resource
Library           ConductorLibrary
Suite Setup       Setup Stress Suite
Suite Teardown    Teardown Stress Suite

*** Variables ***
${SCHEME_A_PORT}    10001
${SCHEME_B_PORT}    10002
${SCHEME_C_PORT}    10003

*** Keywords ***
Setup Stress Suite
    Start Switch Platform    ${CURDIR}
    # Scheme A approves (00), scheme B declines (05); C sinks (unused by load).
    Start Scheme Host    alias=scheme_a    port=${SCHEME_A_PORT}    spec=${SPEC}    de39=00
    Start Scheme Host    alias=scheme_b    port=${SCHEME_B_PORT}    spec=${SPEC}    de39=05
    Start Scheme Host    alias=scheme_c    port=${SCHEME_C_PORT}    spec=${SPEC}    sink=${TRUE}
    Wait For Port    port=${SCHEME_A_PORT}    timeout=10
    Wait For Port    port=${SCHEME_B_PORT}    timeout=10
    Deploy Switch Scenario    ${CURDIR}/scenarios/switch.yaml

Teardown Stress Suite
    Stop Switch Platform

*** Test Cases ***
Mixed Multi-Scheme Load Holds Correlation
    [Documentation]    30 terminals x 100 txns, each txn randomly BIN4 (approve)
    ...                or BIN5 (decline), as fast as possible. Every reply must
    ...                carry its own STAN and the DE39 of the scheme it targeted.
    [Tags]    conductor    stress
    ${rep}=    Run Auth Terminal    target=127.0.0.1:${SWITCH_PORT}    spec=${SPEC}
    ...    report_file=${WORK_DIR}/stress_mixed.json
    ...    mix=4:00,5:05    expect_mti=0210    conns=30    count=100    stan_base=100000    timeout=180
    Log    Mixed load: ${rep}
    Assert All Ok    ${rep}
    Assert De39 Seen    ${rep}    00
    Assert De39 Seen    ${rep}    05
    Log    Achieved ${rep}[achieved_tps] TPS, p99=${rep}[latency_p99_ms]ms over ${rep}[total] txns

High Concurrency Stress Holds Correlation
    [Documentation]    A heavier fan-in: 60 terminals x 150 txns mixed across
    ...                both schemes. Distinct STAN base so keys never collide
    ...                with the previous case.
    [Tags]    conductor    stress
    ${rep}=    Run Auth Terminal    target=127.0.0.1:${SWITCH_PORT}    spec=${SPEC}
    ...    report_file=${WORK_DIR}/stress_high.json
    ...    mix=4:00,5:05    expect_mti=0210    conns=60    count=150    stan_base=500000    timeout=240
    Log    High-concurrency load: ${rep}
    Assert All Ok    ${rep}
    Assert De39 Seen    ${rep}    00
    Assert De39 Seen    ${rep}    05
    Log    Achieved ${rep}[achieved_tps] TPS, p99=${rep}[latency_p99_ms]ms over ${rep}[total] txns
