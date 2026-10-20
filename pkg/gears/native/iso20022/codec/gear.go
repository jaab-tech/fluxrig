// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package codec is the ISO 20022 codec gear. It converts the XML of an ISO
// 20022 message to structured fluxMsg fields and back, driven by a protocol
// spec whose wire source parses the messages and fixes the profile. It does not
// translate between protocols: the translate gear does.
package codec

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/iso20022/rtp"
	"github.com/jaab-tech/fluxrig/pkg/iso20022/spec"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Validation modes, with the same meaning as on the ISO 8583 codec.
const (
	ValidationOff     = "off"
	ValidationWarn    = "warn"
	ValidationEnforce = "enforce"
)

// Metadata keys the codec reads and writes.
const (
	MetaMessage       = "iso20022.message"
	MetaProfile       = "iso20022.profile"
	MetaParticipantID = "iso20022.participant_id"
	metaProtocol      = "codec.protocol"
	metaSpecID        = "codec.spec_id"
	metaSpecVersion   = "codec.spec_version"
	metaSpecHash      = "codec.spec_hash"
	metaViolations    = "codec.violations"
)

// Values of error.reason on a message the codec rejects.
const (
	ReasonViolation = "violation"    // a finding, with validation: enforce
	ReasonFailed    = "codec_failed" // the message could not be decoded or encoded
)

const portError = "error"

// DataKey is the fluxMsg key the raw element paths live under.
const DataKey = "iso20022"

const messageAuto = "auto"

var participantRe = regexp.MustCompile(`^[A-Z0-9]{11}$`)

// Gear is the ISO 20022 codec.
type Gear struct {
	logger  *slog.Logger
	name    string
	emitter sdk.PortEmitter

	spec          *spec.Spec
	message       string // "auto" or a message definition
	direction     string // "auto", "encode", "decode"
	participantID string // empty: from the iso20022.participant_id metadata
	validation    string
	onError       string // "drop", "reject", "kill"

	now func() time.Time

	msgTotal   metric.Int64Counter
	latency    metric.Float64Histogram
	errTotal   metric.Int64Counter
	violations metric.Int64Counter
}

var _ sdk.NativeGear = (*Gear)(nil)

// Init reads the configuration and loads the spec.
func (g *Gear) Init(ctx sdk.GearContext) error {
	g.name = ctx.GearName()
	g.logger = ctx.Logger().With("type", "codec_iso20022")
	g.emitter = ctx.Emitter()
	cfg := ctx.Config()

	ref, _ := cfg["spec_path"].(string)
	if ref == "" {
		return fmt.Errorf("codec_iso20022: missing 'spec_path' in configuration")
	}
	s, err := loadSpec(ctx, ref)
	if err != nil {
		return fmt.Errorf("codec_iso20022: spec %s: %w", ref, err)
	}
	g.spec = s

	g.message = stringOr(cfg, "message", messageAuto)
	if g.message != messageAuto {
		if _, ok := s.Catalog[g.message]; !ok {
			return fmt.Errorf("codec_iso20022: message %q is not in the catalog of spec %s", g.message, s.ID)
		}
	}
	g.direction = stringOr(cfg, "direction", "auto")
	switch g.direction {
	case "auto", "encode", "decode":
	default:
		return fmt.Errorf("codec_iso20022: direction %q: use auto, encode or decode", g.direction)
	}
	g.participantID = stringOr(cfg, "participant_id", "")
	if g.participantID != "" && !participantRe.MatchString(g.participantID) {
		return fmt.Errorf("codec_iso20022: participant_id %q: want 11 upper-case letters or digits", g.participantID)
	}
	g.validation = stringOr(cfg, "validation", ValidationOff)
	switch g.validation {
	case ValidationOff, ValidationWarn, ValidationEnforce:
	default:
		return fmt.Errorf("codec_iso20022: validation %q: use off, warn or enforce", g.validation)
	}
	g.onError = stringOr(cfg, "on_error", "drop")
	switch g.onError {
	case "drop", "reject", "kill":
	default:
		return fmt.Errorf("codec_iso20022: on_error %q: use drop, reject or kill", g.onError)
	}
	if g.now == nil {
		g.now = time.Now
	}

	meter := otel.GetMeterProvider().Meter("fluxrig/gears/codec_iso20022")
	g.msgTotal, _ = meter.Int64Counter("flux.gear.messages_in", metric.WithDescription("Total messages processed by gear"))
	g.latency, _ = meter.Float64Histogram("flux.gear.processing_time_ms", metric.WithDescription("Gear processing latency"), metric.WithUnit("ms"))
	g.errTotal, _ = meter.Int64Counter("flux.gear.errors", metric.WithDescription("Total gear errors"))
	g.violations, _ = meter.Int64Counter("flux.iso20022.violations", metric.WithDescription("Profile rules and spec rules broken by messages"))

	g.logger.Info("Initialized ISO 20022 Codec",
		"spec", ref, "spec_id", s.ID, "spec_version", s.Version, "hash", s.Hash,
		"profile", s.Profile, "message", g.message, "direction", g.direction, "validation", g.validation)
	return nil
}

// Start has nothing to start: the codec is a filter.
func (g *Gear) Start(context.Context, func(*fluxmsg.FluxMsg)) error { return nil }

// Drain has nothing to drain.
func (g *Gear) Drain(context.Context) error { return nil }

// Stop has nothing to stop.
func (g *Gear) Stop() error { return nil }

// Process decodes or encodes one message.
func (g *Gear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	start := time.Now()
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	if msg.Data == nil {
		msg.Data = map[string]any{}
	}
	dir := g.direction
	if dir == "auto" {
		dir = "encode"
		if len(msg.RawPayload) > 0 {
			dir = "decode"
		}
	}
	var (
		id       string
		findings []string
		err      error
	)
	if dir == "decode" {
		id, findings, err = g.decode(msg)
	} else {
		id, findings, err = g.encode(msg)
	}
	attrs := []attribute.KeyValue{attribute.String("gear", g.name), attribute.String("direction", dir), attribute.String("message", id)}
	reason := ReasonFailed
	if err == nil && len(findings) > 0 {
		g.violations.Add(ctx, int64(len(findings)), metric.WithAttributes(attrs...))
		msg.Metadata[metaViolations] = strings.Join(findings, "; ")
		g.logger.Warn("ISO 20022 message breaks rules", "direction", dir, "message", id, "violations", findings, "validation", g.validation)
		if g.validation == ValidationEnforce {
			reason = ReasonViolation
			err = fmt.Errorf("codec_iso20022: %s %s breaks %d rule(s): %s", dir, id, len(findings), strings.Join(findings, "; "))
		}
	}
	if err != nil {
		g.errTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
		g.logger.Warn("Codec processing failed", "direction", dir, "message", id, "error", err, "spec_id", g.spec.ID, "spec_version", g.spec.Version)
		switch g.onError {
		case "drop":
			return nil, nil
		case "reject":
			// The message leaves on the error port, so the scenario can answer it.
			msg.Metadata["error.reason"] = reason
			msg.Metadata["error.gear"] = g.name
			msg.Metadata["error.message"] = err.Error()
			if errEmit := g.emitter.Emit(portError, msg); errEmit != nil {
				g.logger.Warn("Rejected message not emitted; is the error port wired?", "direction", dir, "message", id, "error", errEmit)
			}
			return nil, nil
		}
		return msg, err
	}
	msg.Metadata[metaProtocol] = "iso20022"
	msg.Metadata[MetaMessage] = id
	msg.Metadata[MetaProfile] = g.spec.Profile
	msg.Metadata[metaSpecID] = g.spec.ID
	msg.Metadata[metaSpecVersion] = g.spec.Version
	msg.Metadata[metaSpecHash] = g.spec.Hash
	g.msgTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	g.latency.Record(ctx, float64(time.Since(start).Microseconds())/1000, metric.WithAttributes(attrs...))
	return msg, nil
}

func (g *Gear) decode(msg *fluxmsg.FluxMsg) (string, []string, error) {
	id, fields, err := rtp.Decode(msg.RawPayload)
	if err != nil {
		return "", nil, err
	}
	if g.message != messageAuto && id != g.message {
		return id, nil, fmt.Errorf("codec_iso20022: decoded %s, configured for %s", id, g.message)
	}
	if _, ok := g.spec.Catalog[id]; !ok {
		return id, nil, fmt.Errorf("codec_iso20022: %s is not in the catalog of spec %s", id, g.spec.ID)
	}
	if err = g.write(msg, id, fields); err != nil {
		return id, nil, err
	}
	var findings []string
	if g.validation != ValidationOff {
		findings, err = g.check(id, fields)
	}
	return id, findings, err
}

func (g *Gear) encode(msg *fluxmsg.FluxMsg) (string, []string, error) {
	id := g.message
	if id == messageAuto {
		id = msg.Metadata[MetaMessage]
	}
	if id == "" {
		return "", nil, fmt.Errorf("codec_iso20022: encode: no message definition: set 'message', or let translate set %s", MetaMessage)
	}
	if _, ok := g.spec.Catalog[id]; !ok {
		return id, nil, fmt.Errorf("codec_iso20022: %s is not in the catalog of spec %s", id, g.spec.ID)
	}
	fields, err := g.read(msg, id)
	if err != nil {
		return id, nil, err
	}
	pid := g.participantID
	if pid == "" {
		pid = msg.Metadata[MetaParticipantID]
	}
	if pid == "" {
		return id, nil, fmt.Errorf("codec_iso20022: encode %s: no participant identifier: set 'participant_id', or let translate set %s", id, MetaParticipantID)
	}
	filled, err := rtp.Fill(id, fields, rtp.Context{ParticipantID: pid, Now: g.now()})
	if err != nil {
		return id, nil, err
	}
	var findings []string
	if g.validation != ValidationOff {
		if findings, err = g.check(id, filled); err != nil {
			return id, nil, err
		}
		if g.validation == ValidationEnforce && len(findings) > 0 {
			return id, findings, nil
		}
	}
	raw, err := rtp.Encode(id, filled)
	if err != nil {
		return id, nil, err
	}
	msg.RawPayload = raw
	// What the profile filled is part of the message now: the next gear can
	// read the message identifier, and a conductor can key on it.
	if err := g.write(msg, id, filled); err != nil {
		return id, nil, err
	}
	return id, findings, nil
}

// check returns the profile's findings and the fields the spec requires that
// the message does not have, or forbids that it has.
func (g *Gear) check(id string, fields map[string]string) ([]string, error) {
	findings, err := rtp.Validate(id, fields)
	if err != nil {
		return nil, err
	}
	for path := range g.spec.Fields {
		switch g.spec.Usage(id, path) {
		case spec.UsageMandatory:
			if fields[path] == "" && g.spec.InMessage(id, path) {
				findings = append(findings, fmt.Sprintf("%s is mandatory in %s and missing", g.label(id, path), id))
			}
		case spec.UsageForbidden:
			if fields[path] != "" {
				findings = append(findings, fmt.Sprintf("%s is forbidden in %s and present", g.label(id, path), id))
			}
		}
	}
	sort.Strings(findings)
	return findings, nil
}

func (g *Gear) label(id, path string) string {
	if a := g.spec.Fields[path].Alias; a != "" {
		if p, ok := g.spec.AliasPath(id, a); ok && p == path {
			return a + " (" + path + ")"
		}
	}
	return path
}

// write puts fields into the message: each raw path under DataKey, and each
// alias the spec gives a field of this message.
func (g *Gear) write(msg *fluxmsg.FluxMsg, id string, fields map[string]string) error {
	for path, v := range fields {
		if err := msg.Set(DataKey+"."+path, v); err != nil {
			return fmt.Errorf("codec_iso20022: write %s: %w", path, err)
		}
	}
	for alias, path := range g.spec.Aliases(id) {
		if v, ok := fields[path]; ok {
			if err := msg.Set(alias, v); err != nil {
				return fmt.Errorf("codec_iso20022: write alias %s: %w", alias, err)
			}
		}
	}
	return nil
}

// read collects the fields of a message definition from the message: the raw
// paths under DataKey that the message definition has, then the aliases of the
// message, which win. A raw path of another message definition (a message that
// arrived as a pacs.008 and leaves as a pacs.002) is left out.
func (g *Gear) read(msg *fluxmsg.FluxMsg, id string) (map[string]string, error) {
	valid, err := rtp.ElementPaths(id)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(valid))
	for _, p := range valid {
		known[p] = true
	}
	fields := map[string]string{}
	if tree, ok := msg.Data[DataKey]; ok {
		flattenTree(tree, "", func(path, v string) {
			if known[path] {
				fields[path] = v
			}
		})
	}
	for alias, path := range g.spec.Aliases(id) {
		v, ok := msg.Get(alias)
		if !ok || v == nil {
			continue
		}
		if _, isMap := asMap(v); isMap {
			return nil, fmt.Errorf("codec_iso20022: alias %s holds a structure, not a value", alias)
		}
		fields[path] = fmt.Sprint(v)
	}
	return fields, nil
}

// flattenTree walks a nested map written by fluxMsg.Set and emits each leaf
// with its dotted path. An attribute key ("IntrBkSttlmAmt@Ccy") is a single
// key, so it comes out as written.
func flattenTree(node any, prefix string, emit func(path, v string)) {
	if m, ok := asMap(node); ok {
		for k, child := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flattenTree(child, p, emit)
		}
		return
	}
	if node != nil && prefix != "" {
		emit(prefix, fmt.Sprint(node))
	}
}

func asMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	}
	return nil, false
}

func stringOr(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}

func loadSpec(ctx sdk.GearContext, ref string) (*spec.Spec, error) {
	if !isStoreRef(ref) {
		return spec.LoadFile(filepath.Clean(ref))
	}
	mgr := ctx.Manager()
	if mgr == nil {
		return nil, fmt.Errorf("%q names a store artefact, but this gear has no store to resolve it against", ref)
	}
	content, err := mgr.Load(ctx.Context(), ref)
	if err != nil {
		return nil, fmt.Errorf("resolve %q from the store: %w", ref, err)
	}
	return spec.Load(content)
}

// isStoreRef reports whether a spec reference names a store artefact
// (name:tag or a content hash) rather than a file.
func isStoreRef(ref string) bool {
	if strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml") {
		return false
	}
	if _, err := os.Stat(ref); err == nil {
		return false
	}
	return strings.Contains(ref, ":") || isHex(ref)
}

func isHex(s string) bool {
	if len(s) < 32 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
