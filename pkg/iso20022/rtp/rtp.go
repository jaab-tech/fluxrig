// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package rtp is the wire source "moov-rtp20022": the RTP profile of ISO 20022
// as github.com/moov-io/rtp20022 models it. It reads and writes the envelope
// the profile puts on the wire, a Message element that holds a business
// application header (AppHdr) and one document, and it exchanges the fields of
// both as flat element paths.
//
// Field paths are relative to the document's root element
// ("CdtTrfTxInf.PmtId.InstrId" in a pacs.008), and the header's are under
// "AppHdr." ("AppHdr.To.FIId.FinInstnId.ClrSysMmbId.MmbId").
package rtp

import (
	"encoding/xml"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/moov-io/base"
	"github.com/moov-io/rtp20022/gen/head_001_001_01"
	"github.com/moov-io/rtp20022/gen/messages"
	"github.com/moov-io/rtp20022/gen/pacs_002_001_10"
	"github.com/moov-io/rtp20022/gen/pacs_008_001_08"
	rtplib "github.com/moov-io/rtp20022/pkg/rtp"

	"github.com/jaab-tech/fluxrig/pkg/iso20022/xmlpath"
)

// WireSource is the name a protocol spec uses for this package
// (spec.wire.source), and Profile the profile it implements.
const (
	WireSource = "moov-rtp20022"
	Profile    = "rtp"
)

// HeaderPrefix is the path prefix of the business application header.
const HeaderPrefix = "AppHdr."

const (
	headerNS = "urn:iso:std:iso:20022:tech:xsd:head.001.001.01"
	tchNS    = "urn:tch"

	// generatorField identifies fluxrig as the source in the discretionary
	// part of the identifiers it generates.
	generatorField3 = "FLX"
	generatorField4 = "FLXR"
)

// Header paths the profile fills or requires.
const (
	PathFrom    = HeaderPrefix + "Fr.FIId.FinInstnId.ClrSysMmbId.MmbId"
	PathTo      = HeaderPrefix + "To.FIId.FinInstnId.ClrSysMmbId.MmbId"
	pathBizID   = HeaderPrefix + "BizMsgIdr"
	pathMsgDef  = HeaderPrefix + "MsgDefIdr"
	pathCreated = HeaderPrefix + "CreDt"
)

// Definition is one message definition this wire source carries.
type Definition struct {
	ID        string // "pacs.008.001.08"
	Namespace string // XML namespace of the document
	Envelope  string // element of the document inside <Message>
	Root      string // the document's root element
	newDoc    func() any
	get       func(*messages.Message) any
	set       func(*messages.Message, any)
	fill      func(f map[string]string, c *Context)
}

var definitions = map[string]Definition{
	"pacs.008.001.08": {
		ID:        "pacs.008.001.08",
		Namespace: "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08",
		Envelope:  "CreditTransfer",
		Root:      "FIToFICstmrCdtTrf",
		newDoc:    func() any { return &pacs_008_001_08.DocumentTCH{} },
		get: func(m *messages.Message) any {
			if m.CreditTransfer == nil {
				return nil
			}
			return m.CreditTransfer
		},
		set:  func(m *messages.Message, d any) { m.CreditTransfer = d.(*pacs_008_001_08.DocumentTCH) },
		fill: fillCreditTransfer,
	},
	"pacs.002.001.10": {
		ID:        "pacs.002.001.10",
		Namespace: "urn:iso:std:iso:20022:tech:xsd:pacs.002.001.10",
		Envelope:  "MessageStatusReport",
		Root:      "FIToFIPmtStsRpt",
		newDoc:    func() any { return &pacs_002_001_10.DocumentTCH{} },
		get: func(m *messages.Message) any {
			if m.MessageStatusReport == nil {
				return nil
			}
			return m.MessageStatusReport
		},
		set:  func(m *messages.Message, d any) { m.MessageStatusReport = d.(*pacs_002_001_10.DocumentTCH) },
		fill: fillStatusReport,
	},
}

// Definitions lists the message definitions this wire source carries.
func Definitions() []string {
	out := make([]string, 0, len(definitions))
	for id := range definitions {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Lookup returns a message definition by its identifier.
func Lookup(id string) (Definition, bool) {
	d, ok := definitions[id]
	return d, ok
}

// ByNamespace returns the message definition whose document uses ns.
func ByNamespace(ns string) (Definition, bool) {
	for _, d := range definitions {
		if d.Namespace == ns {
			return d, true
		}
	}
	return Definition{}, false
}

// ElementPaths lists the field paths of a message definition: the document's
// relative to its root, then the header's under HeaderPrefix.
func ElementPaths(id string) ([]string, error) {
	d, ok := definitions[id]
	if !ok {
		return nil, fmt.Errorf("rtp: %q is not a message definition of %s", id, WireSource)
	}
	var out []string
	for _, p := range xmlpath.Paths(d.newDoc()) {
		out = append(out, strings.TrimPrefix(p, d.Root+"."))
	}
	for _, p := range xmlpath.Paths(head_001_001_01.BusinessApplicationHeaderV01TCH{}) {
		out = append(out, HeaderPrefix+p)
	}
	return out, nil
}

// Decode reads an envelope and returns the message definition it carries and
// its fields.
func Decode(raw []byte) (string, map[string]string, error) {
	var m messages.Message
	if err := xml.Unmarshal(raw, &m); err != nil {
		return "", nil, fmt.Errorf("rtp: decode: %w", err)
	}
	var found []Definition
	for _, d := range definitions {
		if d.get(&m) != nil {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		return "", nil, fmt.Errorf("rtp: decode: the envelope carries %d documents this wire source knows, want exactly 1", len(found))
	}
	d := found[0]
	doc, err := xmlpath.Flatten(d.get(&m))
	if err != nil {
		return "", nil, fmt.Errorf("rtp: decode %s: %w", d.ID, err)
	}
	hdr, err := xmlpath.Flatten(m.AppHdr)
	if err != nil {
		return "", nil, fmt.Errorf("rtp: decode %s header: %w", d.ID, err)
	}
	fields := make(map[string]string, len(doc)+len(hdr))
	for p, v := range doc {
		fields[strings.TrimPrefix(p, d.Root+".")] = v
	}
	for p, v := range hdr {
		fields[HeaderPrefix+p] = v
	}
	return d.ID, fields, nil
}

// Context is what the profile needs to fill a message.
type Context struct {
	// ParticipantID is the sender's 11-character participant identifier.
	ParticipantID string
	// Now is the creation time of the message.
	Now time.Time
}

// Fill returns a copy of fields with every element the profile fixes, plus the
// header. A value the fields already have is not changed, with one exception:
// the identity of the message (its message identifier, its creation time, and
// the header's identifier, definition, creation date and sender) always
// belongs to the message being sent, so Fill writes it new. A message decoded
// as one definition and encoded as another (a pacs.008 answered by a
// pacs.002) must not leave with the identity of the message it answers.
func Fill(id string, fields map[string]string, c Context) (map[string]string, error) {
	d, ok := definitions[id]
	if !ok {
		return nil, fmt.Errorf("rtp: %q is not a message definition of %s", id, WireSource)
	}
	if c.ParticipantID == "" {
		return nil, errors.New("rtp: fill: no participant identifier")
	}
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
	out := make(map[string]string, len(fields)+16)
	for p, v := range fields {
		out[p] = v
	}
	created := dateTime(c.Now)
	out["GrpHdr.MsgId"] = rtplib.MessageID(c.Now, c.ParticipantID, generatorField3, nextSerial)
	out["GrpHdr.CreDtTm"] = created
	out[PathFrom] = c.ParticipantID
	out[pathBizID] = rtplib.BusinessID(c.Now, c.ParticipantID, generatorField3, nextSerial)
	out[pathMsgDef] = id
	out[pathCreated] = created
	d.fill(out, &c)
	return out, nil
}

// Encode builds the envelope for a message definition from its fields. It does
// not fill anything: call Fill first.
func Encode(id string, fields map[string]string) ([]byte, error) {
	d, ok := definitions[id]
	if !ok {
		return nil, fmt.Errorf("rtp: %q is not a message definition of %s", id, WireSource)
	}
	m, err := build(d, fields)
	if err != nil {
		return nil, err
	}
	out, err := xml.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("rtp: encode %s: %w", id, err)
	}
	return append([]byte(xml.Header), out...), nil
}

// Validate checks the fields of a message definition against the profile's
// rules, as the library states them. It returns one finding per broken rule.
func Validate(id string, fields map[string]string) ([]string, error) {
	d, ok := definitions[id]
	if !ok {
		return nil, fmt.Errorf("rtp: %q is not a message definition of %s", id, WireSource)
	}
	m, err := build(d, fields)
	if err != nil {
		return nil, err
	}
	var findings []string
	for _, v := range []interface{ Validate() error }{m.AppHdr, d.get(m).(interface{ Validate() error })} {
		verr := v.Validate()
		if verr == nil {
			continue
		}
		var list base.ErrorList
		if errors.As(verr, &list) {
			for _, e := range list {
				findings = append(findings, withholdValue(e.Error()))
			}
			continue
		}
		findings = append(findings, withholdValue(verr.Error()))
	}
	return findings, nil
}

// valueWithheld stands in a finding for the value that broke the rule.
const valueWithheld = "(value withheld)"

// withholdValue removes the field value from a finding of the library's
// validators. They report "<path>: <value> fails <rule>", and the value can
// be an account number or a name, which must not reach a log, the message
// metadata or an error. The path never contains ": ", so the first one ends
// it, and the rule starts at the last " fails ". A finding of another shape
// keeps only its path.
func withholdValue(finding string) string {
	path, rest, ok := strings.Cut(finding, ": ")
	if !ok {
		return valueWithheld
	}
	if i := strings.LastIndex(rest, " fails "); i >= 0 {
		return path + ": " + valueWithheld + rest[i:]
	}
	return path + ": " + valueWithheld
}

// InstructionID generates an instruction identifier in the profile's format,
// for the participant that sends the message.
func InstructionID(participantID string, now time.Time) string {
	return rtplib.InstructionID(now, participantID, generatorField4, nextSerial)
}

func build(d Definition, fields map[string]string) (*messages.Message, error) {
	doc := d.newDoc()
	hdr := map[string]string{}
	body := map[string]string{}
	for p, v := range fields {
		if strings.HasPrefix(p, HeaderPrefix) {
			hdr[strings.TrimPrefix(p, HeaderPrefix)] = v
			continue
		}
		body[d.Root+"."+p] = v
	}
	if err := xmlpath.Assign(doc, body); err != nil {
		return nil, fmt.Errorf("rtp: %s: %w", d.ID, err)
	}
	m := &messages.Message{}
	if err := xmlpath.Assign(&m.AppHdr, hdr); err != nil {
		return nil, fmt.Errorf("rtp: %s header: %w", d.ID, err)
	}
	d.set(m, doc)
	m.Xmlns = []xml.Attr{
		{Name: xml.Name{Local: "xmlns"}, Value: tchNS},
		{Name: xml.Name{Local: "xmlns:head"}, Value: headerNS},
		{Name: xml.Name{Local: "xmlns:" + messages.NamespacePrefixMap[d.Namespace]}, Value: d.Namespace},
	}
	return m, nil
}

func fillCreditTransfer(f map[string]string, c *Context) {
	const tx = "CdtTrfTxInf."
	put(f, "GrpHdr.IntrBkSttlmDt", c.Now.Format(time.DateOnly))
	put(f, "GrpHdr.NbOfTxs", "1")
	put(f, "GrpHdr.TtlIntrBkSttlmAmt", f[tx+"IntrBkSttlmAmt"])
	put(f, "GrpHdr.TtlIntrBkSttlmAmt@Ccy", f[tx+"IntrBkSttlmAmt@Ccy"])
	put(f, "GrpHdr.SttlmInf.SttlmMtd", "CLRG")
	put(f, "GrpHdr.SttlmInf.ClrSys.Cd", "TCH")
	put(f, tx+"PmtTpInf.SvcLvl.Cd", "SDVA")
	put(f, tx+"ChrgBr", "SLEV")
	put(f, tx+"InstgAgt.FinInstnId.ClrSysMmbId.MmbId", f[tx+"DbtrAgt.FinInstnId.ClrSysMmbId.MmbId"])
	put(f, tx+"InstdAgt.FinInstnId.ClrSysMmbId.MmbId", f[tx+"CdtrAgt.FinInstnId.ClrSysMmbId.MmbId"])
}

func fillStatusReport(f map[string]string, c *Context) {
	put(f, "TxInfAndSts.AccptncDtTm", dateTime(c.Now))
	put(f, "OrgnlGrpInfAndSts.OrgnlNbOfTxs", "1")
}

// put sets a field only when it is absent and the value is known.
func put(f map[string]string, path, value string) {
	if _, ok := f[path]; ok || value == "" {
		return
	}
	f[path] = value
}

func dateTime(t time.Time) string {
	b, err := rtplib.ISODateTime(t.UTC()).MarshalText()
	if err != nil {
		return t.UTC().Format("2006-01-02T15:04:05")
	}
	return string(b)
}

// serial is the numeric part of the identifiers this process generates. It
// starts from the clock, so a restart does not repeat the identifiers of the
// previous run, and it only goes up, so two messages never share one.
var serial atomic.Uint64

func init() {
	serial.Store(uint64(time.Now().UnixMicro()))
}

func nextSerial(length int) string {
	n := serial.Add(1)
	s := fmt.Sprintf("%020d", n)
	if length < len(s) {
		return s[len(s)-length:]
	}
	return s
}
