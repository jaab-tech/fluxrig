// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package translation

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

func loadSpec(t *testing.T, path string) *Spec {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Load(raw, FileResolver)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	if err := s.LoadLookups(FileResolver); err != nil {
		t.Fatalf("lookups: %v", err)
	}
	return s
}

func fixedClock(t *testing.T) {
	t.Helper()
	Clock = func() time.Time { return time.Date(2026, 10, 7, 14, 30, 0, 0, time.UTC) }
	t.Cleanup(func() { Clock = time.Now })
}

// request0200 is the fluxMsg that codec_iso8583 makes of the tutorial's 0200.
func request0200(merchant string) *fluxmsg.FluxMsg {
	m := fluxmsg.New()
	m.Metadata["iso8583.mti"] = "0200"
	m.Metadata["codec.spec_id"] = "iso8583-v87-ascii"
	m.Metadata["codec.spec_version"] = "3.0.0"
	for k, v := range map[string]string{
		"mti": "0200", "proc_code": "500000", "txn_amount": "000000004250",
		"txn_currency": "840", "stan": "000017", "rrn": "000000000017",
		"terminal_id": "TERM0001", "merchant_id": merchant,
	} {
		m.Data[k] = v
	}
	return m
}

func str(t *testing.T, m *fluxmsg.FluxMsg, key string) string {
	t.Helper()
	return readString(m, key)
}

func TestPaymentRequest(t *testing.T) {
	fixedClock(t)
	s := loadSpec(t, "testdata/billpay-rtp.translation.yaml")
	m := request0200("BILLER000000001")
	tr, err := s.Select("auto", m)
	if err != nil || tr.Name != "payment_request" {
		t.Fatalf("select: %v, %v", tr, err)
	}
	findings, err := tr.Apply(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings: %v", findings)
	}
	want := map[string]string{
		"settlement_amount": "42.50", "settlement_currency": "USD",
		"end_to_end_id": "000000000017", "tx_id": "000000000017",
		"creditor_name": "Example Water Utility", "creditor_account": "7700123456",
		"creditor_agent": "990000002", "debtor_agent": "990000001",
		"remittance_info": "TERM0001", "local_instrument": "STANDARD",
		// The translator removes nothing: the ISO 8583 fields stay.
		"stan": "000017", "txn_amount": "000000004250",
	}
	for k, v := range want {
		if got := str(t, m, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if id := str(t, m, "instr_id"); !regexp.MustCompile(`^2026100799000000101BFLXR[0-9]{11}$`).MatchString(id) {
		t.Errorf("instr_id %q is not in the format of the profile", id)
	}
	for k, v := range map[string]string{
		"iso20022.message":        "pacs.008.001.08",
		"iso20022.participant_id": "99000000101",
		"translate.spec_id":       "billpay-rtp",
		"translate.name":          "payment_request",
	} {
		if m.Metadata[k] != v {
			t.Errorf("metadata %s = %q, want %q", k, m.Metadata[k], v)
		}
	}
}

func TestPaymentRequestUnknownBiller(t *testing.T) {
	s := loadSpec(t, "testdata/billpay-rtp.translation.yaml")
	m := request0200("BILLER000000999")
	findings, err := s.Translations["payment_request"].Apply(m)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(findings, "\n")
	for _, a := range []string{"creditor_name", "creditor_account", "creditor_agent"} {
		if !strings.Contains(joined, a+" is required") {
			t.Errorf("no finding for %s in %q", a, joined)
		}
	}
}

func TestSourceSpecVersionFinding(t *testing.T) {
	s := loadSpec(t, "testdata/billpay-rtp.translation.yaml")
	m := request0200("BILLER000000001")
	m.Metadata["codec.spec_version"] = "2.1.0"
	findings, _ := s.Translations["payment_request"].Apply(m)
	if len(findings) != 1 || !strings.Contains(findings[0], "decoded with iso8583-v87-ascii 2.1.0") {
		t.Fatalf("findings: %v", findings)
	}
}

func TestPaymentStatus(t *testing.T) {
	s := loadSpec(t, "testdata/billpay-rtp.translation.yaml")
	for _, c := range []struct{ status, reason, want string }{
		{"ACTC", "", "00"},
		{"RCVD", "", "09"},
		{"RJCT", "AM02", "13"},
		{"RJCT", "AM04", "51"},
		{"RJCT", "XX99", "05"},
	} {
		// The pacs.002 after the conductor restored the parked ISO 8583 fields.
		m := request0200("BILLER000000001")
		m.Metadata = map[string]string{"iso20022.message": "pacs.002.001.10"}
		m.Data["tx_status"] = c.status
		if c.reason != "" {
			m.Data["status_reason"] = c.reason
		}
		tr, err := s.Select("auto", m)
		if err != nil || tr.Name != "payment_status" {
			t.Fatalf("select: %v, %v", tr, err)
		}
		findings, err := tr.Apply(m)
		if err != nil {
			t.Fatal(err)
		}
		// The parked fields and the response code complete the 0210.
		if len(findings) != 0 {
			t.Errorf("%s/%s: findings %v", c.status, c.reason, findings)
		}
		if got := str(t, m, "resp_code"); got != c.want {
			t.Errorf("%s/%s: resp_code %q, want %q", c.status, c.reason, got, c.want)
		}
		if m.Metadata["iso8583.mti"] != "0210" || str(t, m, "mti") != "0210" {
			t.Errorf("the target is not a 0210: %v, mti %q", m.Metadata, str(t, m, "mti"))
		}
	}
}

func TestBindingsReadTheArrivingMessage(t *testing.T) {
	s := loadSpec(t, "testdata/rail-sim.translation.yaml")
	m := fluxmsg.New()
	m.Metadata["iso20022.message"] = "pacs.008.001.08"
	for k, v := range map[string]string{
		"msg_id": "M1", "creation_time": "2026-10-07T14:30:00", "instr_id": "I1",
		"sender_participant": "99000000101", "receiver_participant": "99000000201",
		"instructing_agent": "990000001", "instructed_agent": "990000002",
	} {
		m.Data[k] = v
	}
	findings, err := s.Translations["acknowledge"].Apply(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings: %v", findings)
	}
	for k, v := range map[string]string{
		"instructing_agent": "990000002", "instructed_agent": "990000001",
		"receiver_participant": "99000000101", "orig_msg_id": "M1", "tx_status": "ACTC",
	} {
		if got := str(t, m, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if m.Metadata["iso20022.message"] != "pacs.002.001.10" || m.Metadata["iso20022.participant_id"] != "99000000201" {
		t.Errorf("metadata: %v", m.Metadata)
	}
}

func TestLoadRejects(t *testing.T) {
	base, err := os.ReadFile("testdata/billpay-rtp.translation.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, old, new, want string }{
		{"unknown target alias", `to: "tx_id"`, `to: "tx_ident"`, `"tx_ident" is not an alias of iso20022-rtp`},
		{"unknown source alias", `from: "terminal_id"`, `from: "terminal"`, `from "terminal" is not an alias of iso8583-v87-ascii`},
		{"unknown message", `to: "rail:pacs.008.001.08"`, `to: "rail:pacs.009.001.08"`, `iso20022-rtp has no message pacs.009.001.08`},
		{"unknown spec", `from: "card:0200"`, `from: "atm:0200"`, `from "atm:0200" names no spec of specs`},
		{"undeclared table", `table: "tx_status_to_resp_code"`, `table: "codes"`, `table "codes" is not declared`},
		{"two kinds", `{ to: "local_instrument", value: "STANDARD" }`, `{ to: "local_instrument", value: "STANDARD", from: "rrn" }`, `want exactly one of`},
		{"bad generate", `generate: "instr_id"`, `generate: "serial"`, `generate "serial"`},
		{"unknown lookup field", `lookup: "billers.name"`, `lookup: "billers.title"`, `lookup billers has no field title`},
		{"written twice", `{ to: "tx_id", from: "rrn" }`, `{ to: "end_to_end_id", from: "stan" }`, `end_to_end_id is written twice`},
		{"reply to the wrong message", `to: "card:0210"`, `to: "card:0110"`, `a reply to pacs.008.001.08 goes back as 0210, not 0110`},
		{"amount without currency", `format: "amount", currency: "txn_currency"`, `format: "amount"`, `format amount needs one from and a currency`},
		{"bad participant", `participant_id: "99000000101"`, `participant_id: "990"`, `participant_id "990"`},
		{"unknown key", `participant_id: "99000000101"`, "participant_id: \"99000000101\"\n  owner: \"x\"", `field owner not found`},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := strings.Replace(string(base), c.old, c.new, 1)
			if raw == string(base) {
				t.Fatalf("the fixture has no %q", c.old)
			}
			_, err := Load([]byte(raw), FileResolver)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestConvertAmount(t *testing.T) {
	for _, c := range []struct{ amount, cur, kind, wantAmount, wantCur string }{
		{"000000004250", "840", "iso20022", "42.50", "USD"},
		{"000000000005", "840", "iso20022", "0.05", "USD"},
		{"000000000000", "USD", "iso20022", "0.00", "USD"},
		{"000000001000", "392", "iso20022", "1000", "JPY"},
		{"000000012345", "414", "iso20022", "12.345", "KWD"},
		{"42.50", "USD", "iso8583", "000000004250", "840"},
		{"42.5", "USD", "iso8583", "000000004250", "840"},
		{"1000", "JPY", "iso8583", "000000001000", "392"},
		{"12.345", "KWD", "iso8583", "000000012345", "414"},
	} {
		a, cur, err := convertAmount(c.amount, c.cur, c.kind)
		if err != nil || a != c.wantAmount || cur != c.wantCur {
			t.Errorf("%s %s to %s: %q %q %v, want %q %q", c.amount, c.cur, c.kind, a, cur, err, c.wantAmount, c.wantCur)
		}
	}
	for _, c := range []struct{ amount, cur, kind string }{
		{"4250", "XXX", "iso20022"},
		{"42.50", "840", "iso20022"},
		{"42.505", "USD", "iso8583"},
		{"1.5", "JPY", "iso8583"},
		{"12345678901234", "USD", "iso8583"},
		{"4x.00", "USD", "iso8583"},
	} {
		if _, _, err := convertAmount(c.amount, c.cur, c.kind); err == nil {
			t.Errorf("%s %s to %s: no error", c.amount, c.cur, c.kind)
		}
	}
}

func TestRequiredFromTheMatrix(t *testing.T) {
	s := loadSpec(t, "testdata/billpay-rtp.translation.yaml")
	has := func(p Protocol, message, alias string) bool {
		for _, r := range p.Required(message) {
			if r.Alias == alias {
				return true
			}
		}
		return false
	}
	if card := s.Protocols["card"]; !has(card, "0210", "resp_code") || !has(card, "0210", "stan") || has(card, "0210", "mti") {
		t.Errorf("0210 requires %v", card.Required("0210"))
	}
	if rail := s.Protocols["rail"]; !has(rail, "pacs.002.001.10", "instr_id") || has(rail, "pacs.002.001.10", "creditor_name") {
		t.Errorf("pacs.002 requires %v", rail.Required("pacs.002.001.10"))
	}
}
