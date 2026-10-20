# Copyright (c) 2026 JAAB Tech SAS, Uruguay
# SPDX-License-Identifier: Apache-2.0

*** Settings ***
Documentation     The ISO 8583 to ISO 20022 bridge, run as the tutorial runs it.
...
...               A terminal sends a 0200 bill payment. The bridge translates it
...               to a pacs.008 for the RTP rail, the simulated rail answers with
...               a pacs.002, and the bridge returns a 0210. Each case asserts
...               the response code the tutorial shows. The scenario, the specs
...               and the records come from examples/, so a change that breaks
...               the tutorial fails here.
Resource          billpay_rtp.resource
Suite Setup       Start Bridge Platform    ${CURDIR}
Suite Teardown    Stop Bridge Platform

*** Variables ***
${BILLER}           BILLER000000001
${UNKNOWN_BILLER}   BILLER000000999
# 42.50 and 1250.00 USD in minor units. The simulated bank accepts up to 1000.00.
${UNDER_LIMIT}      000000004250
${OVER_LIMIT}       000000125000

*** Test Cases ***
A Payment The Bank Accepts Gets 00
    [Documentation]    pacs.002 TxSts ACTC, translated to response code 00.
    ${report}=    Pay A Bill    000123    ${UNDER_LIMIT}    ${BILLER}
    Response Code Should Be    ${report}    00

A Payment Over The Bank Limit Gets 13
    [Documentation]    pacs.002 RJCT with reason AM02, which the table gives as 13.
    ${report}=    Pay A Bill    000124    ${OVER_LIMIT}    ${BILLER}
    Response Code Should Be    ${report}    13

An Unknown Biller Is Declined Before The Rail
    [Documentation]    The lookup finds no record, so creditor_name stays empty.
    ...                to-rail rejects the message on its error port, and the
    ...                decline answers 03 (invalid merchant). A 03 can only come
    ...                from that path: the rail answers with TxSts, and the
    ...                conductor's errors give 68 or 91.
    ${report}=    Pay A Bill    000125    ${UNDER_LIMIT}    ${UNKNOWN_BILLER}
    Response Code Should Be    ${report}    03

A Rail That Is Down Gets 91
    [Documentation]    With rack-rail gone, rail reports its link down, and the
    ...                conductor sends the request to the decline at once with
    ...                error.reason no_destination. This case stops rack-rail, so
    ...                it runs last.
    Stop Process    rack-rail
    Wait Until Keyword Succeeds    30s    500ms    Rail Link Should Be Down
    ${report}=    Pay A Bill    000126    ${UNDER_LIMIT}    ${BILLER}
    Response Code Should Be    ${report}    91
