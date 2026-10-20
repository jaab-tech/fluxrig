// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package rtp

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var at = time.Date(2026, 10, 7, 14, 21, 3, 0, time.UTC)

// businessFields are what a translation supplies for a pacs.008: everything
// the profile does not fix.
func businessFields() map[string]string {
	return map[string]string{
		"CdtTrfTxInf.PmtId.InstrId":                        InstructionID("99000000101", at),
		"CdtTrfTxInf.PmtId.EndToEndId":                     "628012000123",
		"CdtTrfTxInf.PmtId.TxId":                           "628012000123",
		"CdtTrfTxInf.PmtTpInf.LclInstrm.Prtry":             "STANDARD",
		"CdtTrfTxInf.PmtTpInf.CtgyPurp.Prtry":              "CONSUMER",
		"CdtTrfTxInf.IntrBkSttlmAmt":                       "42.50",
		"CdtTrfTxInf.IntrBkSttlmAmt@Ccy":                   "USD",
		"CdtTrfTxInf.Dbtr.Nm":                              "Example Payments Operator",
		"CdtTrfTxInf.DbtrAcct.Id.Othr.Id":                  "5500001111",
		"CdtTrfTxInf.DbtrAgt.FinInstnId.ClrSysMmbId.MmbId": "990000001",
		"CdtTrfTxInf.Cdtr.Nm":                              "Example Water Utility",
		"CdtTrfTxInf.CdtrAcct.Id.Othr.Id":                  "7700123456",
		"CdtTrfTxInf.CdtrAgt.FinInstnId.ClrSysMmbId.MmbId": "990000002",
		PathTo: "99000000901",
	}
}

func TestDecodeReadsEnvelopeAndHeader(t *testing.T) {
	raw, err := os.ReadFile("testdata/pacs008.xml")
	if err != nil {
		t.Fatal(err)
	}
	id, f, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if id != "pacs.008.001.08" {
		t.Fatalf("id = %q", id)
	}
	for p, want := range map[string]string{
		"CdtTrfTxInf.PmtId.InstrId": "2026100799000000101AAA00000000017",
		"CdtTrfTxInf.Cdtr.Nm":       "Example Water Utility",
		"GrpHdr.MsgId":              "M2026100799000000101BAAA00000000017",
		PathFrom:                    "99000000101",
		PathTo:                      "99000000901",
		HeaderPrefix + "MsgDefIdr":  "pacs.008.001.08",
	} {
		if f[p] != want {
			t.Errorf("%s = %q, want %q", p, f[p], want)
		}
	}
}

func TestFillEncodeDecodeIsAValidMessage(t *testing.T) {
	filled, err := Fill("pacs.008.001.08", businessFields(), Context{ParticipantID: "99000000101", Now: at})
	if err != nil {
		t.Fatal(err)
	}
	findings, err := Validate("pacs.008.001.08", filled)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("a filled message breaks the profile: %v", findings)
	}
	raw, err := Encode("pacs.008.001.08", filled)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `<Message xmlns="urn:tch"`) || !strings.Contains(string(raw), "<CreditTransfer>") {
		t.Fatalf("not a TCH envelope:\n%s", raw)
	}
	id, back, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode what we encoded: %v\n%s", err, raw)
	}
	if id != "pacs.008.001.08" {
		t.Fatalf("id = %q", id)
	}
	for p, v := range filled {
		if back[p] != v {
			t.Errorf("%s = %q after the round trip, want %q", p, back[p], v)
		}
	}
	for p, want := range map[string]string{
		"GrpHdr.NbOfTxs":                                    "1",
		"GrpHdr.SttlmInf.ClrSys.Cd":                         "TCH",
		"GrpHdr.TtlIntrBkSttlmAmt":                          "42.50",
		"CdtTrfTxInf.ChrgBr":                                "SLEV",
		"CdtTrfTxInf.InstdAgt.FinInstnId.ClrSysMmbId.MmbId": "990000002",
		PathFrom:                   "99000000101",
		HeaderPrefix + "MsgDefIdr": "pacs.008.001.08",
	} {
		if back[p] != want {
			t.Errorf("filled %s = %q, want %q", p, back[p], want)
		}
	}
}

func TestFillKeepsValuesButRenewsIdentity(t *testing.T) {
	f := businessFields()
	f["CdtTrfTxInf.ChrgBr"] = "SLEV"
	f["GrpHdr.MsgId"] = "M2026100799000000101BAAA00000000001"
	filled, err := Fill("pacs.008.001.08", f, Context{ParticipantID: "99000000101", Now: at})
	if err != nil {
		t.Fatal(err)
	}
	if filled["CdtTrfTxInf.ChrgBr"] != "SLEV" || filled["CdtTrfTxInf.Dbtr.Nm"] != "Example Payments Operator" {
		t.Errorf("Fill changed a business value: %v", filled)
	}
	if filled["GrpHdr.MsgId"] == "M2026100799000000101BAAA00000000001" {
		t.Error("Fill kept the identity of another message: MsgId must be new")
	}
	if _, ok := businessFields()["GrpHdr.MsgId"]; ok {
		t.Error("Fill changed its input")
	}
}

func TestValidateReportsBrokenRules(t *testing.T) {
	f := businessFields()
	f["CdtTrfTxInf.IntrBkSttlmAmt@Ccy"] = "EUR"
	delete(f, PathTo)
	filled, err := Fill("pacs.008.001.08", f, Context{ParticipantID: "99000000101", Now: at})
	if err != nil {
		t.Fatal(err)
	}
	findings, err := Validate("pacs.008.001.08", filled)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(findings, "\n")
	for _, want := range []string{"Ccy", "To"} {
		if !strings.Contains(joined, want) {
			t.Errorf("findings do not mention %q:\n%s", want, joined)
		}
	}
}

func TestStatusReportRoundTrip(t *testing.T) {
	f := map[string]string{
		// what a decoded pacs.008 leaves behind: its own identity
		"GrpHdr.MsgId":                                      "M2026100799000000101BAAA00000000017",
		PathFrom:                                            "99000000101",
		"OrgnlGrpInfAndSts.OrgnlMsgId":                      "M2026100799000000101BAAA00000000017",
		"OrgnlGrpInfAndSts.OrgnlMsgNmId":                    "pacs.008.001.08",
		"OrgnlGrpInfAndSts.OrgnlCreDtTm":                    "2026-10-07T14:21:03",
		"TxInfAndSts.OrgnlInstrId":                          InstructionID("99000000101", at),
		"TxInfAndSts.TxSts":                                 "RJCT",
		"TxInfAndSts.StsRsnInf.Rsn.Cd":                      "AM02",
		"TxInfAndSts.InstgAgt.FinInstnId.ClrSysMmbId.MmbId": "990000002",
		"TxInfAndSts.InstdAgt.FinInstnId.ClrSysMmbId.MmbId": "990000001",
		PathTo: "99000000101",
	}
	filled, err := Fill("pacs.002.001.10", f, Context{ParticipantID: "99000000201", Now: at})
	if err != nil {
		t.Fatal(err)
	}
	if findings, _ := Validate("pacs.002.001.10", filled); len(findings) != 0 {
		t.Fatalf("a filled status report breaks the profile: %v", findings)
	}
	raw, err := Encode("pacs.002.001.10", filled)
	if err != nil {
		t.Fatal(err)
	}
	id, back, err := Decode(raw)
	if err != nil || id != "pacs.002.001.10" {
		t.Fatalf("decode: %q %v", id, err)
	}
	if back["GrpHdr.MsgId"] == "M2026100799000000101BAAA00000000017" || back[PathFrom] != "99000000201" {
		t.Errorf("the reply kept the identity of the message it answers: MsgId %q, Fr %q", back["GrpHdr.MsgId"], back[PathFrom])
	}
	if back["TxInfAndSts.TxSts"] != "RJCT" || back["TxInfAndSts.StsRsnInf.Rsn.Cd"] != "AM02" {
		t.Errorf("status lost: %v", back)
	}
}

func TestIdentifiersHaveTheProfileFormat(t *testing.T) {
	instr := regexp.MustCompile(`^20261007` + `99000000101` + `BFLXR[0-9]{11}$`)
	a, b := InstructionID("99000000101", at), InstructionID("99000000101", at)
	if !instr.MatchString(a) {
		t.Errorf("InstructionID %q does not have the profile format", a)
	}
	if a == b {
		t.Error("two instruction identifiers are equal")
	}
}

func TestElementPathsAndLookup(t *testing.T) {
	ps, err := ElementPaths("pacs.008.001.08")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CdtTrfTxInf.PmtId.InstrId", "CdtTrfTxInf.IntrBkSttlmAmt@Ccy", PathTo} {
		if !slices.Contains(ps, want) {
			t.Errorf("ElementPaths misses %q", want)
		}
	}
	if _, err := ElementPaths("pain.001.001.09"); err == nil {
		t.Error("an unknown message definition has element paths")
	}
	if d, ok := ByNamespace("urn:iso:std:iso:20022:tech:xsd:pacs.002.001.10"); !ok || d.ID != "pacs.002.001.10" {
		t.Error("ByNamespace does not find pacs.002")
	}
	if got := Definitions(); !slices.Equal(got, []string{"pacs.002.001.10", "pacs.008.001.08"}) {
		t.Errorf("Definitions = %v", got)
	}
}

// A finding names the field and the rule, never the value: an account number
// or a name in a log, the message metadata or an error is a leak.
func TestValidateWithholdsTheValue(t *testing.T) {
	const account = "ACCT0123456789012345678901234567890123456789" // over the 34 the profile allows
	fields := map[string]string{
		"CdtTrfTxInf.PmtId.InstrId": "2026100799000000101BFLXR00000000001", "CdtTrfTxInf.PmtId.EndToEndId": "E2E", "CdtTrfTxInf.PmtId.TxId": "T",
		"CdtTrfTxInf.IntrBkSttlmAmt": "42.50", "CdtTrfTxInf.IntrBkSttlmAmt@Ccy": "USD",
		"CdtTrfTxInf.Dbtr.Nm": "Example Debtor", "CdtTrfTxInf.DbtrAcct.Id.Othr.Id": account,
		"CdtTrfTxInf.DbtrAgt.FinInstnId.ClrSysMmbId.MmbId": "990000001", "CdtTrfTxInf.Cdtr.Nm": "Example Creditor",
		"CdtTrfTxInf.CdtrAcct.Id.Othr.Id": "7700123456", "CdtTrfTxInf.CdtrAgt.FinInstnId.ClrSysMmbId.MmbId": "990000002",
	}
	filled, err := Fill("pacs.008.001.08", fields, Context{ParticipantID: "99000000101", Now: time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	findings, err := Validate("pacs.008.001.08", filled)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range findings {
		if strings.Contains(f, account) || strings.Contains(f, "0123456789") {
			t.Errorf("a finding carries the account number: %s", f)
		}
		if strings.Contains(f, "DbtrAcct") && strings.Contains(f, "maxLength 34") && strings.Contains(f, "(value withheld)") {
			found = true
		}
	}
	if !found {
		t.Errorf("no finding names the debtor account and its rule: %v", findings)
	}
}

func TestWithholdValue(t *testing.T) {
	for in, want := range map[string]string{
		"A -> B.Id: SECRET fails validation with length 44 <= required maxLength 34": "A -> B.Id: (value withheld) fails validation with length 44 <= required maxLength 34",
		"A.Ccy: has: colon fails enumeration validation":                             "A.Ccy: (value withheld) fails enumeration validation",
		"A.Nm: x fails y fails enumeration validation":                               "A.Nm: (value withheld) fails enumeration validation",
		"A.Nm: something unexpected":                                                 "A.Nm: (value withheld)",
		"no separator at all":                                                        "(value withheld)",
	} {
		if got := withholdValue(in); got != want {
			t.Errorf("withholdValue(%q) = %q, want %q", in, got, want)
		}
	}
}
