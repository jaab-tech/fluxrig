// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package xmlpath

import (
	"encoding/xml"
	"os"
	"slices"
	"testing"

	"github.com/moov-io/rtp20022/gen/messages"
	"github.com/moov-io/rtp20022/gen/pacs_008_001_08"
)

func decodeFixture(t *testing.T) messages.Message {
	t.Helper()
	raw, err := os.ReadFile("testdata/pacs008.xml")
	if err != nil {
		t.Fatal(err)
	}
	var m messages.Message
	if err := xml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return m
}

func TestPathsUseSchemaElementNames(t *testing.T) {
	ps := Paths(pacs_008_001_08.DocumentTCH{})
	for _, want := range []string{
		"FIToFICstmrCdtTrf.GrpHdr.MsgId",
		"FIToFICstmrCdtTrf.CdtTrfTxInf.PmtId.InstrId",
		"FIToFICstmrCdtTrf.CdtTrfTxInf.IntrBkSttlmAmt",
		"FIToFICstmrCdtTrf.CdtTrfTxInf.IntrBkSttlmAmt@Ccy",
		"FIToFICstmrCdtTrf.CdtTrfTxInf.CdtrAgt.FinInstnId.ClrSysMmbId.MmbId",
	} {
		if !slices.Contains(ps, want) {
			t.Errorf("Paths misses %q", want)
		}
	}
	if slices.Contains(ps, "FIToFICstmrCdtTrf.CdtTrfTxInf.InstrForCdtrAgt.Cd") {
		t.Error("a repeated element must not have a flat path")
	}
}

func TestFlattenReadsTheDocument(t *testing.T) {
	m := decodeFixture(t)
	got, err := Flatten(m.CreditTransfer)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"FIToFICstmrCdtTrf.GrpHdr.MsgId":                   "M2026100799000000101BAAA00000000017",
		"FIToFICstmrCdtTrf.CdtTrfTxInf.IntrBkSttlmAmt":     "42.50",
		"FIToFICstmrCdtTrf.CdtTrfTxInf.IntrBkSttlmAmt@Ccy": "USD",
		"FIToFICstmrCdtTrf.CdtTrfTxInf.Cdtr.Nm":            "Example Water Utility",
		"FIToFICstmrCdtTrf.GrpHdr.IntrBkSttlmDt":           "2026-10-07",
	} {
		if got[path] != want {
			t.Errorf("%s = %q, want %q", path, got[path], want)
		}
	}
	if _, ok := got["FIToFICstmrCdtTrf.CdtTrfTxInf.UltmtDbtr.Nm"]; ok {
		t.Error("an absent optional element must not appear")
	}
}

func TestAssignRoundTrips(t *testing.T) {
	m := decodeFixture(t)
	want, err := Flatten(m.CreditTransfer)
	if err != nil {
		t.Fatal(err)
	}
	var doc pacs_008_001_08.DocumentTCH
	if err = Assign(&doc, want); err != nil {
		t.Fatal(err)
	}
	got, err := Flatten(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("round trip has %d paths, want %d", len(got), len(want))
	}
	for p, v := range want {
		if got[p] != v {
			t.Errorf("%s = %q after the round trip, want %q", p, got[p], v)
		}
	}
	if err := doc.Validate(); err != nil {
		t.Errorf("the reassigned document fails the profile: %v", err)
	}
}

func TestAssignRefusesUnknownPaths(t *testing.T) {
	var doc pacs_008_001_08.DocumentTCH
	for _, p := range []string{
		"FIToFICstmrCdtTrf.GrpHdr.NoSuchElement",
		"FIToFICstmrCdtTrf.GrpHdr.MsgId.Below",
		"FIToFICstmrCdtTrf.CdtTrfTxInf.InstrForCdtrAgt.Cd",
	} {
		if err := Assign(&doc, map[string]string{p: "x"}); err == nil {
			t.Errorf("Assign(%q) succeeded, want an error", p)
		}
	}
	if err := Assign(doc, map[string]string{}); err == nil {
		t.Error("Assign to a non-pointer succeeded")
	}
}
