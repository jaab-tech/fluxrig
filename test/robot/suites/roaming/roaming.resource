# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     Shared setup for the roaming enrichment suite: one Mixer and
...               one Rack carrying the enrichment pipeline together with both
...               the telco simulator, which is an ordinary gear like any other.
Library           fluxrigLibrary
Library           ISO8583Library
Library           OperatingSystem
Library           ConductorLibrary
Library           Collections
Library           Process

*** Variables ***
${MIXER_PORT}       8090
${SCHEME_PORT}      8586
${CAMARA_PORT}      19500
${AUTHORIZER_PORT}  18590
${WORK_DIR}         ${EMPTY}
# Must equal the key the enrolment fixtures were derived under. Changing it
# without regenerating them makes every card miss and every outcome `unknown`,
# which is a passing-looking failure, so it is asserted in the setup below.
${HMAC_KEY}         robot-suite-enrolment-key

*** Keywords ***
Start Roaming Platform
    [Documentation]    Brings up the platform and deploys the three scenarios:
    ...                the enrichment under test, the operator endpoint, and the
    ...                issuer's authorizer.
    [Arguments]    ${suite_dir}
    Force Cleanup Environment
    ${wd}=    Setup Workspace    ${suite_dir}    output_dir=${OUTPUT_DIR}
    Set Suite Variable    ${WORK_DIR}    ${wd}
    Set Suite Variable    ${SUITE_DIR}    ${suite_dir}

    # Each Rack resolves spec_path relative to its own working directory, so
    # every process that decodes needs its own copy.
    Create Directory    ${WORK_DIR}/fluxrig/specs
    Copy File    ${suite_dir}/specs/scheme.yaml    ${WORK_DIR}/fluxrig/specs/scheme.yaml
    Copy File    ${suite_dir}/specs/issuer.yaml    ${WORK_DIR}/fluxrig/specs/issuer.yaml

    Set Environment Variable    ENROLMENT_HMAC_KEY    ${HMAC_KEY}

    Generate Cluster Key    work_dir=${WORK_DIR}/mixer
    Start Mixer    config_file=${suite_dir}/configs/mixer/mixer.toml    work_dir=${WORK_DIR}/mixer    alias=mixer
    Wait For Healthy    port=${MIXER_PORT}

    # The issuer's authorizer is not a Rack: it is iso8583-tool, the same host
    # ISO 8583 host simulator the ISO 8583 suite uses, told to report DE 48 back in DE 42 so a
    # test can see which outcome arrived. A counterparty played by fluxrig would
    # make the suite a conversation with itself.
    Start Scheme Host    alias=authorizer    port=${AUTHORIZER_PORT}
    ...    spec=${suite_dir}/specs/issuer.yaml    move=48:42    log_file=${WORK_DIR}/authorizer.log
    Wait For Port    port=${AUTHORIZER_PORT}    timeout=30

    FOR    ${r}    IN    fluxrig    telco
        Start Rack    config_file=${suite_dir}/configs/racks/${r}.toml    work_dir=${WORK_DIR}/${r}    mixer_home=${WORK_DIR}/mixer    alias=rack-${r}
        Wait For Rack Registration    mixer_port=${MIXER_PORT}    rack_name=rack-${r}
    END

    # One import for all three Racks. A scenario spans them, and each Rack runs
    # only the gears whose deploy target names it; a second import would replace
    # this one rather than add to it.
    Import Scenario    mixer_port=${MIXER_PORT}    file_path=${suite_dir}/scenarios/roaming_suite.yaml
    Wait For Port    port=${CAMARA_PORT}    timeout=30
    Wait For Port    port=${AUTHORIZER_PORT}    timeout=30
    Wait For Port    port=${SCHEME_PORT}    timeout=30
    # Not a sleep. The switch's client dials the authorizer on its own schedule,
    # and a fixed wait is either too short on a loaded machine or wasted on an
    # idle one. The client logs the connection, so that is the condition.
    Wait Until Keyword Succeeds    30s    500ms
    ...    Uplink Should Be Connected

Uplink Should Be Connected
    [Documentation]    Succeeds once authorizer-out reports its connection.
    ${found}=    Grep File    ${WORK_DIR}/fluxrig/logs/process_stdout.log    connected
    Should Not Be Empty    ${found}    msg=the switch has not dialled the authorizer yet

Restart Authorizer With Delay
    [Documentation]    Replaces the authorizer with one that answers late, so a
    ...                reply lands after its correlation entry has expired. The
    ...                Rack's client reconnects on its own (reconnect_wait: 1s).
    [Arguments]    ${delay}
    Run Keyword And Ignore Error    Stop Conductor Process    authorizer
    Start Scheme Host    alias=authorizer_slow    port=${AUTHORIZER_PORT}
    ...    spec=${SUITE_DIR}/specs/issuer.yaml    move=48:42    delay=${delay}
    ...    log_file=${WORK_DIR}/authorizer_slow.log
    Wait For Port    port=${AUTHORIZER_PORT}    timeout=30
    # The switch's client must notice the new host before a transaction is sent,
    # or it writes into a socket that no longer exists.
    Sleep    2s    reason=reconnect_wait is 1s; give the client a cycle to redial

Stop Roaming Platform
    Run Keyword And Ignore Error    Stop Conductor Process    authorizer
    FOR    ${r}    IN    fluxrig    telco
        Run Keyword And Ignore Error    Stop Process    rack-${r}
    END
    Run Keyword And Ignore Error    Stop Process    mixer

# Stress / Load Keywords
Start Auth Fleet
    [Documentation]    Starts multiple auth terminals in the background, each with
    ...                a different PAN to exercise different telco outcomes.
    ...                Returns a list of [aliases, reports].
    [Arguments]    ${pan_list}    ${target}    ${spec}    ${work_dir}    ${conns}=30    ${count}=100    ${stan_base}=100000    ${timeout}=180    ${rate}=0    ${merchant_country}=UY    ${mti}=0200    ${expect_mti}=0210    ${prefix}=stress
    @{aliases}=    Create List
    @{reports}=    Create List
    ${index}=    Set Variable    0
    ${pan_list_len}=    Get Length    ${pan_list}
    ${i}=    Set Variable    0
    ${timeout_str}=    Evaluate    str(${timeout}) + "s"
    # DE 43 puts the country in its last two chars; Acceptor Location places it
    # there in the 40-char field. Passing a raw string lets fitField right-pad it
    # and bury the country in trailing spaces.
    ${de43}=    Acceptor Location    SHOP    ${merchant_country}
    WHILE    ${i} < ${pan_list_len}
        ${pan}=    Get From List    ${pan_list}    ${i}
        # The prefix keeps two fleets running at once from colliding on alias
        # or report file, which is what the reversal case needs: same trace
        # range, different message class.
        ${alias}=    Set Variable    ${prefix}_${index}
        ${report}=    Set Variable    ${work_dir}/${prefix}_${index}.json
        Append To List    ${aliases}    ${alias}
        Append To List    ${reports}    ${report}
        # Both the STAN and the terminal ID (DE 41) the tool emits derive from
        # stan_base, and the correlation key is (mti_class, DE 41, STAN). Sharing
        # one base across terminals makes every key collide, so give each its own
        # block, exactly the conns*count span one terminal consumes.
        ${term_stan_base}=    Evaluate    ${stan_base} + ${index} * ${conns} * ${count}
        Start Auth Terminal    alias=${alias}    target=${target}    spec=${spec}
        ...    report_file=${report}
        ...    pan=${pan}
        ...    conns=${conns}    count=${count}
        ...    stan_base=${term_stan_base}    timeout=${timeout_str}
        ...    rate=${rate}    de43=${de43}    mti=${mti}    expect_mti=${expect_mti}
        ${index}=    Evaluate    ${index} + 1
        ${i}=    Evaluate    ${i} + 1
    END
    @{result}=    Create List    ${aliases}    ${reports}
    RETURN    ${result}

Wait Auth Fleet
    [Documentation]    Waits for every terminal in the fleet and returns one merged
    ...                report. Counters are summed; the latency percentiles are the
    ...                worst any single terminal saw, because averaging percentiles
    ...                across terminals would understate the tail.
    [Arguments]    ${fleet_result}    ${timeout}=180
    @{aliases}=    Get From List    ${fleet_result}    0
    @{reports}=    Get From List    ${fleet_result}    1
    ${merged}=    Create Dictionary
    ...    terminals=0    count=0    total=0    ok=0    cross_wired=0    failed=0
    ...    dropped=0    reconnects=0    elapsed_sec=0.0    achieved_tps=0.0
    ...    latency_p50_ms=0.0    latency_p99_ms=0.0
    ${merged.by_de39}=    Create Dictionary
    ${merged.by_de42}=    Create Dictionary
    ${count}=    Get Length    ${aliases}
    ${i}=    Set Variable    0
    WHILE    ${i} < ${count}
        ${alias}=    Get From List    ${aliases}    ${i}
        ${rep}=    Wait Auth Terminal    alias=${alias}    timeout=${timeout}
        # A terminal killed on timeout writes no report, so read_auth_report
        # returns {} and its total/ok both default to 0, which passes ok==total
        # while silently dropping its work. Fail loudly instead.
        ${rc}=    Get From Dictionary    ${rep}    rc    default=-999
        ${has_total}=    Run Keyword And Return Status    Dictionary Should Contain Key    ${rep}    total
        IF    ${rc} != 0 or not ${has_total}
            Fail    terminal ${alias} did not complete cleanly (rc=${rc}, report present=${has_total})
        END
        FOR    ${key}    IN    terminals    count    total    ok    cross_wired    failed    dropped    reconnects
            ${v}=    Get From Dictionary    ${rep}    ${key}    default=0
            ${old}=    Get From Dictionary    ${merged}    ${key}
            ${sum}=    Evaluate    ${old} + ${v}
            Set To Dictionary    ${merged}    ${key}    ${sum}
        END
        Merge Counter Map    ${merged.by_de39}    ${rep}    by_de39
        Merge Counter Map    ${merged.by_de42}    ${rep}    by_de42
        FOR    ${key}    IN    latency_p50_ms    latency_p99_ms
            ${v}=    Get From Dictionary    ${rep}    ${key}    default=0
            ${old}=    Get From Dictionary    ${merged}    ${key}
            ${worst}=    Evaluate    max(${old}, ${v})
            Set To Dictionary    ${merged}    ${key}    ${worst}
        END
        ${tps}=    Get From Dictionary    ${rep}    achieved_tps    default=0
        ${old_tps}=    Get From Dictionary    ${merged}    achieved_tps
        ${sum_tps}=    Evaluate    ${old_tps} + ${tps}
        Set To Dictionary    ${merged}    achieved_tps    ${sum_tps}
        ${el}=    Get From Dictionary    ${rep}    elapsed_sec    default=0
        ${old_el}=    Get From Dictionary    ${merged}    elapsed_sec
        ${max_el}=    Evaluate    max(${old_el}, ${el})
        Set To Dictionary    ${merged}    elapsed_sec    ${max_el}
        ${i}=    Evaluate    ${i} + 1
    END
    RETURN    ${merged}

Merge Counter Map
    [Documentation]    Adds one report's {value: count} map into an accumulator.
    ...                Summing with Evaluate rather than string interpolation: the
    ...                latter stores "0+5" and every later read of it fails.
    [Arguments]    ${target}    ${report}    ${field}
    ${src}=    Get From Dictionary    ${report}    ${field}    default=${EMPTY}
    ${is_dict}=    Evaluate    isinstance($src, dict)
    IF    not ${is_dict}    RETURN
    ${keys}=    Get Dictionary Keys    ${src}
    FOR    ${k}    IN    @{keys}
        ${n}=    Get From Dictionary    ${src}    ${k}
        ${old}=    Get From Dictionary    ${target}    ${k}    default=0
        ${sum}=    Evaluate    ${old} + ${n}
        Set To Dictionary    ${target}    ${k}    ${sum}
    END

Authorize
    [Documentation]    Sends one 0100 for a card and a merchant country, and
    ...                returns the reply as hex. The card selects the operator's
    ...                behaviour, because the number it is enrolled under ends in
    ...                the digits the telco simulator switches on.
    [Arguments]    ${pan}    ${merchant_country}    ${stan}
    ${location}=    Acceptor Location    SHOP    ${merchant_country}
    ${msg}=    Build ISO Message    0100
    ...    f2=${pan}    f3=000000    f4=000000010000    f7=0101120000
    ...    f11=${stan}    f18=5411    f22=051
    ...    f41=TERM0001    f43=${location}
    ${reply}=    Send ISO Message    ${msg}    target=127.0.0.1:${SCHEME_PORT}    header_len=2
    RETURN    ${reply}
