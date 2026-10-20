// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package translate

import (
	"context"
	"strings"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	iso20022codec "github.com/jaab-tech/fluxrig/pkg/gears/native/iso20022/codec"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

const (
	billpaySpec = "testdata/billpay-rtp.translation.yaml"
	railSimSpec = "testdata/rail-sim.translation.yaml"
	rtpSpec     = "../../../../examples/specs/iso20022-rtp.yaml"
)

func newGear(t *testing.T, cfg map[string]any) *Gear {
	t.Helper()
	g := &Gear{}
	if err := g.Init(sdk.NewMockGearContext(cfg)); err != nil {
		t.Fatalf("init %v: %v", cfg, err)
	}
	return g
}

// newCodec makes a codec with a fixed direction, as the tutorial's scenario
// configures them: after a decode, the message keeps its raw bytes, so an auto
// codec would decode it again.
func newCodec(t *testing.T, direction string) *iso20022codec.Gear {
	t.Helper()
	g := &iso20022codec.Gear{}
	if err := g.Init(sdk.NewMockGearContext(map[string]any{"spec_path": rtpSpec, "direction": direction, "validation": "enforce", "on_error": "reject"})); err != nil {
		t.Fatal(err)
	}
	return g
}

func request0200(merchant, amount string) *fluxmsg.FluxMsg {
	m := fluxmsg.New()
	m.Metadata["iso8583.mti"] = "0200"
	m.Metadata["codec.spec_id"] = "iso8583-v87-ascii"
	m.Metadata["codec.spec_version"] = "3.0.0"
	for k, v := range map[string]string{
		"mti": "0200", "proc_code": "500000", "txn_amount": amount,
		"txn_currency": "840", "stan": "000017", "rrn": "000000000017",
		"terminal_id": "TERM0001", "merchant_id": merchant,
	} {
		m.Data[k] = v
	}
	return m
}

func process(t *testing.T, g sdk.NativeGear, m *fluxmsg.FluxMsg) *fluxmsg.FluxMsg {
	t.Helper()
	out, err := g.Process(context.Background(), m)
	if err != nil {
		t.Fatalf("%T: %v", g, err)
	}
	if out == nil {
		t.Fatalf("%T dropped the message", g)
	}
	return out
}

// wire keeps only what crosses the network: the encoded bytes.
func wire(m *fluxmsg.FluxMsg) *fluxmsg.FluxMsg {
	out := fluxmsg.New()
	out.RawPayload = m.RawPayload
	return out
}

func get(m *fluxmsg.FluxMsg, key string) string {
	v, _ := m.Get(key)
	s, _ := v.(string)
	return s
}

// TestBridgeRoundTrip runs the two legs of the tutorial in memory: the 0200
// becomes a pacs.008 on the wire, the rail answers it with a pacs.002, and the
// pacs.002 becomes the response code of the 0210.
func TestBridgeRoundTrip(t *testing.T) {
	toRail := newGear(t, map[string]any{"spec_path": billpaySpec, "translation": "payment_request", "validation": "enforce", "on_error": "reject"})
	ack := newGear(t, map[string]any{"spec_path": railSimSpec, "validation": "enforce", "on_error": "reject"})
	toCard := newGear(t, map[string]any{"spec_path": billpaySpec, "translation": "payment_status", "validation": "enforce", "on_error": "reject"})
	encode, decode := newCodec(t, "encode"), newCodec(t, "decode")

	req := process(t, toRail, request0200("BILLER000000001", "000000004250"))
	instrID := get(req, "instr_id")
	pacs008 := process(t, encode, req)
	if !strings.Contains(string(pacs008.RawPayload), "<ct:IntrBkSttlmAmt Ccy=\"USD\">42.50</ct:IntrBkSttlmAmt>") {
		t.Fatalf("pacs.008 has no settlement amount of 42.50 USD:\n%s", pacs008.RawPayload)
	}

	// The rail: decode, answer, encode.
	railIn := process(t, decode, wire(pacs008))
	if get(railIn, "creditor_name") != "Example Water Utility" {
		t.Fatalf("the rail decoded creditor_name %q", get(railIn, "creditor_name"))
	}
	pacs002 := process(t, encode, process(t, ack, railIn))

	// Back at the bridge: decode, then the conductor restores the parked fields.
	back := process(t, decode, wire(pacs002))
	if get(back, "instr_id") != instrID {
		t.Fatalf("instr_id %q came back as %q: the conductor cannot find the ticket", instrID, get(back, "instr_id"))
	}
	for k, v := range map[string]string{
		"proc_code": "500000", "txn_amount": "000000004250", "txn_currency": "840",
		"stan": "000017", "rrn": "000000000017", "terminal_id": "TERM0001", "merchant_id": "BILLER000000001",
	} {
		back.Data[k] = v
	}
	resp := process(t, toCard, back)
	if get(resp, "resp_code") != "00" || resp.Metadata["iso8583.mti"] != "0210" {
		t.Fatalf("0210: resp_code %q, mti %q", get(resp, "resp_code"), resp.Metadata["iso8583.mti"])
	}
	if v := resp.Metadata[metaViolations]; v != "" {
		t.Fatalf("violations: %s", v)
	}
}

// emitContext captures what a gear emits on its named ports.
type emitContext struct {
	sdk.GearContext
	emitted map[string][]*fluxmsg.FluxMsg
}

func (c *emitContext) Emitter() sdk.PortEmitter { return c }

func (c *emitContext) Emit(port string, msg *fluxmsg.FluxMsg) error {
	c.emitted[port] = append(c.emitted[port], msg)
	return nil
}

// With on_error: reject, a rejected message leaves on the error port, so the
// scenario can answer the terminal instead of letting it time out.
func TestRejectEmitsOnTheErrorPort(t *testing.T) {
	ctx := &emitContext{
		GearContext: sdk.NewMockGearContext(map[string]any{"spec_path": billpaySpec, "validation": "enforce", "on_error": "reject"}),
		emitted:     map[string][]*fluxmsg.FluxMsg{},
	}
	g := &Gear{}
	if err := g.Init(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := g.Process(context.Background(), request0200("BILLER000000999", "000000004250"))
	if out != nil || err != nil {
		t.Fatalf("out %v, err %v; want nothing on out", out, err)
	}
	rejected := ctx.emitted["error"]
	if len(rejected) != 1 {
		t.Fatalf("emitted %v, want one message on error", ctx.emitted)
	}
	m := rejected[0].Metadata
	if m["error.reason"] != ReasonViolation || m["error.gear"] != g.name ||
		!strings.Contains(m["error.message"], "creditor_name is required") ||
		!strings.Contains(m[metaViolations], "creditor_name is required") {
		t.Fatalf("metadata %v", m)
	}
	// The terminal's fields are still there, for the decline to answer with.
	if get(rejected[0], "stan") != "000017" {
		t.Errorf("the rejected message lost the STAN")
	}
}

func TestWarnRecordsAndContinues(t *testing.T) {
	g := newGear(t, map[string]any{"spec_path": billpaySpec, "validation": "warn"})
	out := process(t, g, request0200("BILLER000000999", "000000004250"))
	if !strings.Contains(out.Metadata[metaViolations], "creditor_name is required") {
		t.Fatalf("violations: %q", out.Metadata[metaViolations])
	}
}

func TestDropIsTheDefault(t *testing.T) {
	g := newGear(t, map[string]any{"spec_path": billpaySpec, "validation": "enforce"})
	out, err := g.Process(context.Background(), request0200("BILLER000000999", "000000004250"))
	if out != nil || err != nil {
		t.Fatalf("out %v, err %v; want the message dropped", out, err)
	}
}

func TestInitRefusesBadConfig(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"no spec":             {},
		"missing spec":        {"spec_path": "testdata/none.yaml"},
		"unknown translation": {"spec_path": billpaySpec, "translation": "refund"},
		"bad validation":      {"spec_path": billpaySpec, "validation": "strict"},
		"bad on_error":        {"spec_path": billpaySpec, "on_error": "retry"},
		"store without store": {"spec_path": "billpay-rtp:v1.0.0"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := (&Gear{}).Init(sdk.NewMockGearContext(cfg)); err == nil {
				t.Fatal("Init accepted the configuration")
			}
		})
	}
}
