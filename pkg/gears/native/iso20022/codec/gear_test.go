// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/iso20022/rtp"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

const refSpec = "../../../../../examples/specs/iso20022-rtp.yaml"

var at = time.Date(2026, 10, 7, 14, 21, 3, 0, time.UTC)

func newGear(t *testing.T, cfg map[string]any) *Gear {
	t.Helper()
	full := map[string]any{"spec_path": refSpec}
	for k, v := range cfg {
		full[k] = v
	}
	g := &Gear{now: func() time.Time { return at }}
	if err := g.Init(sdk.NewMockGearContext(full)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return g
}

func newMsg() *fluxmsg.FluxMsg {
	return &fluxmsg.FluxMsg{Data: map[string]any{}, Metadata: map[string]string{}}
}

func get(t *testing.T, m *fluxmsg.FluxMsg, key string) string {
	t.Helper()
	v, ok := m.Get(key)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func TestInitRefusesBadConfig(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"no spec":            {"spec_path": ""},
		"message not listed": {"spec_path": refSpec, "message": "pacs.009.001.08"},
		"participant format": {"spec_path": refSpec, "participant_id": "123"},
		"validation":         {"spec_path": refSpec, "validation": "strict"},
		"on_error":           {"spec_path": refSpec, "on_error": "ignore"},
		"direction":          {"spec_path": refSpec, "direction": "both"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := (&Gear{}).Init(sdk.NewMockGearContext(cfg)); err == nil {
				t.Fatal("Init succeeded")
			}
		})
	}
}

func TestDecodeWritesPathsAliasesAndMetadata(t *testing.T) {
	g := newGear(t, map[string]any{"direction": "decode"})
	raw, err := os.ReadFile("testdata/pacs008.xml")
	if err != nil {
		t.Fatal(err)
	}
	m := newMsg()
	m.RawPayload = raw
	out, err := g.Process(context.Background(), m)
	if err != nil || out == nil {
		t.Fatalf("Process: %v", err)
	}
	for key, want := range map[string]string{
		"iso20022.CdtTrfTxInf.Cdtr.Nm":            "Example Water Utility",
		"iso20022.CdtTrfTxInf.IntrBkSttlmAmt@Ccy": "USD",
		"creditor_name":                           "Example Water Utility",
		"settlement_amount":                       "42.50",
		"instr_id":                                "2026100799000000101AAA00000000017",
		"receiver_participant":                    "99000000901",
	} {
		if got := get(t, out, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]string{
		MetaMessage:          "pacs.008.001.08",
		MetaProfile:          "rtp",
		"codec.protocol":     "iso20022",
		"codec.spec_id":      "iso20022-rtp",
		"codec.spec_version": "1.0.0",
	} {
		if out.Metadata[key] != want {
			t.Errorf("metadata %s = %q, want %q", key, out.Metadata[key], want)
		}
	}
}

// translated is a message as the translate gear leaves it before the encode.
func translated() *fluxmsg.FluxMsg {
	m := newMsg()
	for alias, v := range map[string]string{
		"instr_id":             rtp.InstructionID("99000000101", at),
		"end_to_end_id":        "628012000123",
		"tx_id":                "628012000123",
		"local_instrument":     "STANDARD",
		"category_purpose":     "CONSUMER",
		"settlement_amount":    "42.50",
		"settlement_currency":  "USD",
		"debtor_name":          "Example Payments Operator",
		"debtor_account":       "5500001111",
		"debtor_agent":         "990000001",
		"creditor_name":        "Example Water Utility",
		"creditor_account":     "7700123456",
		"creditor_agent":       "990000002",
		"receiver_participant": "99000000901",
		"pan":                  "4111111111111111", // an ISO 8583 field: never encoded
	} {
		_ = m.Set(alias, v)
	}
	m.Metadata[MetaMessage] = "pacs.008.001.08"
	m.Metadata[MetaParticipantID] = "99000000101"
	return m
}

func TestEncodeBuildsAValidMessageFromAliases(t *testing.T) {
	g := newGear(t, map[string]any{"direction": "encode", "validation": "enforce", "on_error": "reject"})
	out, err := g.Process(context.Background(), translated())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	raw := string(out.RawPayload)
	if !strings.Contains(raw, "<CreditTransfer>") || strings.Contains(raw, "4111111111111111") {
		t.Fatalf("bad envelope, or the PAN leaked:\n%s", raw)
	}
	id, fields, err := rtp.Decode(out.RawPayload)
	if err != nil || id != "pacs.008.001.08" {
		t.Fatalf("decode: %q %v", id, err)
	}
	if findings, _ := rtp.Validate(id, fields); len(findings) != 0 {
		t.Fatalf("encoded message breaks the profile: %v", findings)
	}
	if get(t, out, "msg_id") == "" || get(t, out, "msg_id") != fields["GrpHdr.MsgId"] {
		t.Errorf("msg_id alias = %q, want the generated %q", get(t, out, "msg_id"), fields["GrpHdr.MsgId"])
	}
	if out.Metadata["codec.violations"] != "" {
		t.Errorf("violations on a valid message: %s", out.Metadata["codec.violations"])
	}
}

// emitContext captures what the codec emits on its named ports.
type emitContext struct {
	sdk.GearContext
	emitted map[string][]*fluxmsg.FluxMsg
}

func (c *emitContext) Emitter() sdk.PortEmitter { return c }

func (c *emitContext) Emit(port string, msg *fluxmsg.FluxMsg) error {
	c.emitted[port] = append(c.emitted[port], msg)
	return nil
}

func TestEnforceRefusesAMissingMandatoryField(t *testing.T) {
	ctx := &emitContext{
		GearContext: sdk.NewMockGearContext(map[string]any{"spec_path": refSpec, "direction": "encode", "validation": "enforce", "on_error": "reject"}),
		emitted:     map[string][]*fluxmsg.FluxMsg{},
	}
	g := &Gear{now: func() time.Time { return at }}
	if err := g.Init(ctx); err != nil {
		t.Fatal(err)
	}
	m := translated()
	delete(m.Data, "creditor_name")
	out, err := g.Process(context.Background(), m)
	if out != nil || err != nil {
		t.Fatalf("reject: out %v err %v, want nothing on out", out, err)
	}
	rejected := ctx.emitted["error"]
	if len(rejected) != 1 {
		t.Fatalf("emitted %v, want one message on error", ctx.emitted)
	}
	md := rejected[0].Metadata
	if md["error.reason"] != ReasonViolation || !strings.Contains(md["error.message"], "creditor_name") {
		t.Errorf("metadata %v: want reason %s and a message that names the field", md, ReasonViolation)
	}
	if len(rejected[0].RawPayload) != 0 {
		t.Error("a rejected message must leave unencoded")
	}

	drop := newGear(t, map[string]any{"direction": "encode", "validation": "enforce"})
	m = translated()
	delete(m.Data, "creditor_name")
	if out, err := drop.Process(context.Background(), m); out != nil || err != nil {
		t.Errorf("drop: out %v err %v, want nil, nil", out, err)
	}
}

func TestWarnRecordsAndContinues(t *testing.T) {
	g := newGear(t, map[string]any{"direction": "encode", "validation": "warn"})
	m := translated()
	_ = m.Set("settlement_currency", "EUR")
	out, err := g.Process(context.Background(), m)
	if err != nil || out == nil {
		t.Fatalf("warn must let the message continue: %v", err)
	}
	if !strings.Contains(out.Metadata["codec.violations"], "Ccy") {
		t.Errorf("violations = %q, want the currency finding", out.Metadata["codec.violations"])
	}
}

func TestAnswerADecodedCreditTransfer(t *testing.T) {
	dec := newGear(t, map[string]any{"direction": "decode"})
	enc := newGear(t, map[string]any{"direction": "encode", "validation": "enforce", "on_error": "reject"})
	raw, _ := os.ReadFile("testdata/pacs008.xml")
	m := newMsg()
	m.RawPayload = raw
	m, err := dec.Process(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	// what the simulated bank's translation adds
	for alias, v := range map[string]string{
		"orig_msg_id":          get(t, m, "msg_id"),
		"orig_msg_name":        "pacs.008.001.08",
		"orig_creation_time":   get(t, m, "creation_time"),
		"tx_status":            "ACTC",
		"instructing_agent":    "990000002",
		"instructed_agent":     "990000001",
		"receiver_participant": "99000000101",
	} {
		_ = m.Set(alias, v)
	}
	m.Metadata[MetaMessage] = "pacs.002.001.10"
	m.Metadata[MetaParticipantID] = "99000000201"
	m.RawPayload = nil
	out, err := enc.Process(context.Background(), m)
	if err != nil {
		t.Fatalf("encode the answer: %v", err)
	}
	id, f, err := rtp.Decode(out.RawPayload)
	if err != nil || id != "pacs.002.001.10" {
		t.Fatalf("decode the answer: %q %v", id, err)
	}
	if f["TxInfAndSts.OrgnlInstrId"] != "2026100799000000101AAA00000000017" {
		t.Errorf("OrgnlInstrId = %q, want the credit transfer's InstrId", f["TxInfAndSts.OrgnlInstrId"])
	}
	if f["GrpHdr.MsgId"] == "M2026100799000000101BAAA00000000017" || f[rtp.PathFrom] != "99000000201" {
		t.Errorf("the answer kept the identity of the credit transfer: MsgId %q Fr %q", f["GrpHdr.MsgId"], f[rtp.PathFrom])
	}
	if _, ok := f["CdtTrfTxInf.Cdtr.Nm"]; ok {
		t.Error("a pacs.008 field leaked into the pacs.002")
	}
}
