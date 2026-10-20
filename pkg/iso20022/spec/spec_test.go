// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"strings"
	"testing"
)

const refSpec = "../../../examples/specs/iso20022-rtp.yaml"

func TestReferenceSpecLoads(t *testing.T) {
	s, err := LoadFile(refSpec)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "iso20022-rtp" || s.Profile != "rtp" || len(s.Hash) != 64 {
		t.Fatalf("header: id %q profile %q hash %q", s.ID, s.Profile, s.Hash)
	}
	for _, c := range []struct{ msg, alias, path string }{
		{"pacs.008.001.08", "instr_id", "CdtTrfTxInf.PmtId.InstrId"},
		{"pacs.002.001.10", "instr_id", "TxInfAndSts.OrgnlInstrId"},
		{"pacs.008.001.08", "msg_id", "GrpHdr.MsgId"},
		{"pacs.002.001.10", "msg_id", "GrpHdr.MsgId"},
		{"pacs.008.001.08", "receiver_participant", "AppHdr.To.FIId.FinInstnId.ClrSysMmbId.MmbId"},
		{"pacs.008.001.08", "settlement_currency", "CdtTrfTxInf.IntrBkSttlmAmt@Ccy"},
	} {
		if got, ok := s.AliasPath(c.msg, c.alias); !ok || got != c.path {
			t.Errorf("%s %s = %q, want %q", c.msg, c.alias, got, c.path)
		}
	}
	if _, ok := s.AliasPath("pacs.002.001.10", "creditor_name"); ok {
		t.Error("creditor_name must not be an alias of pacs.002")
	}
	if s.Usage("pacs.008.001.08", "CdtTrfTxInf.Cdtr.Nm") != UsageMandatory {
		t.Error("Cdtr.Nm must be mandatory in pacs.008")
	}
	if !s.Fields["CdtTrfTxInf.CdtrAcct.Id.Othr.Id"].Masked() {
		t.Error("an account identifier must be masked")
	}
}

// base is a valid spec the negative cases change in one place each.
const base = `
spec:
  id: "t"
  version: "1.0.0"
  wire: {source: "moov-rtp20022"}
  messages:
    catalog:
      "pacs.008.001.08": {flow: request, pairs_with: "pacs.002.001.10"}
      "pacs.002.001.10": {flow: response, pairs_with: "pacs.008.001.08"}
  enums:
    tx_status: {closed: true, values: {"ACTC": {name: "Accepted"}}}
  fields:
    "CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name"}
    "TxInfAndSts.TxSts": {alias: "tx_status", values_ref: "tx_status"}
`

func TestBaseLoads(t *testing.T) {
	if _, err := Load([]byte(base)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejects(t *testing.T) {
	for _, c := range []struct {
		name, old, new, want string
	}{
		{"unknown element", `"CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name"}`, `"CdtTrfTxInf.Cdtr.Name": {alias: "creditor_name"}`, `CdtTrfTxInf.Cdtr.Name`},
		{"wire attribute", `"CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name"}`, `"CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name", length: 140}`, `length`},
		{"alias twice in one message", `"TxInfAndSts.TxSts": {alias: "tx_status", values_ref: "tx_status"}`, `"TxInfAndSts.TxSts": {alias: "tx_status", values_ref: "tx_status"}
    "TxInfAndSts.OrgnlInstrId": {alias: "tx_status"}`, `alias "tx_status"`},
		{"values_ref", `values_ref: "tx_status"`, `values_ref: "no_such_set"`, `no_such_set`},
		{"message not in the wire source", `"pacs.002.001.10": {flow: response, pairs_with: "pacs.008.001.08"}`, `"pacs.002.001.10": {flow: response, pairs_with: "pacs.008.001.08"}
      "pain.001.001.09": {flow: request}`, `pain.001.001.09`},
		{"pairs_with not symmetric", `"pacs.002.001.10": {flow: response, pairs_with: "pacs.008.001.08"}`, `"pacs.002.001.10": {flow: response}`, `pairs_with`},
		{"log_mask off on pii", `"CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name"}`, `"CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name", sensitivity: pii, log_mask: false}`, `log_mask`},
		{"unknown wire source", `source: "moov-rtp20022"`, `source: "moov-cbpr"`, `moov-cbpr`},
		{"bad version", `version: "1.0.0"`, `version: "1.0"`, `spec.version`},
		{"matrix names a message outside the catalog", `"CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name"}`, `"CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name", messages: [{message: "pacs.009.001.08", usage: mandatory}]}`, `pacs.009.001.08`},
		{"element of another message", `"CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name"}`, `"CdtTrfTxInf.Cdtr.Nm": {alias: "creditor_name", messages: [{message: "pacs.002.001.10", usage: mandatory}]}`, `has no such element`},
		{"translation kind", `id: "t"`, `kind: "translation"
  id: "t"`, `spec.kind`},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := strings.Replace(base, c.old, c.new, 1)
			if src == base {
				t.Fatalf("the case did not change the spec")
			}
			_, err := Load([]byte(src))
			if err == nil {
				t.Fatal("loaded, want an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not name %q", err, c.want)
			}
		})
	}
}
