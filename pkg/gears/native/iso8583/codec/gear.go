// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moov-io/iso8583"
	"github.com/moov-io/iso8583/field"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
	"github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/manager"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Config holds the configuration for the ISO8583 Codec gear.
type Config struct {
	Name      string `json:"name"`
	SpecPath  string `json:"spec_path"`
	Direction string `json:"direction"`
	OnError   string `json:"on_error"`
	// Validation decides what the spec's semantic rules do to traffic:
	// "off" (default), "warn" or "enforce". See validation constants.
	Validation string `json:"validation"`
}

// What the spec's semantic rules do to traffic.
//
// The default is off, and deliberately: a message accepted yesterday must not be
// rejected today because the code was upgraded, and warning on every message is
// a cost an operator did not ask for. "warn" is how you find out whether your
// spec matches your traffic; "enforce" is the decision you make afterwards.
const (
	ValidationOff     = "off"
	ValidationWarn    = "warn"
	ValidationEnforce = "enforce"
)

// Gear implements the NativeGear interface for the ISO8583 Codec.
type Gear struct {
	logger *slog.Logger
	name   string
	emit   func(*fluxmsg.FluxMsg)

	moovSpec  *iso8583.MessageSpec
	meta      *sdl.FieldMeta
	direction string // "auto", "encode", "decode"
	onError   string // "drop", "reject", "kill"

	// validation is off, warn or enforce; validator is nil when off, so the
	// hot path costs a nil check rather than a decision.
	validation string
	validator  *sdl.Validator

	// Telemetry
	meter       metric.Meter
	msgTotal    metric.Int64Counter
	latency     metric.Float64Histogram
	fieldsCount metric.Int64Histogram
	errTotal    metric.Int64Counter
	violations  metric.Int64Counter
}

// Init loads configuration and prepares the gear.
func (g *Gear) Init(ctx sdk.GearContext) error {
	g.name = ctx.GearName()
	g.logger = ctx.Logger().With("type", "codec_iso8583")
	config := ctx.Config()

	// 1. Load Spec
	specPath, _ := config["spec_path"].(string)
	if specPath == "" {
		// Fallback to legacy key "spec"
		specPath, _ = config["spec"].(string)
	}

	if specPath == "" {
		return fmt.Errorf("missing 'spec_path' in configuration")
	}

	moovSpec, meta, specContent, err := loadConfiguredSpec(ctx, specPath)
	if err != nil {
		return fmt.Errorf("failed to load SDL spec from %s: %w", specPath, err)
	}
	g.moovSpec = moovSpec
	g.meta = meta

	// 2. Config options
	g.direction, _ = config["direction"].(string)
	if g.direction == "" {
		g.direction = "auto"
	}

	g.onError, _ = config["on_error"].(string)
	if g.onError == "" {
		g.onError = "drop"
	}

	g.validation, _ = config["validation"].(string)
	if g.validation == "" {
		g.validation = ValidationOff
	}
	switch g.validation {
	case ValidationOff:
	case ValidationWarn, ValidationEnforce:
		// Compiled once, at boot. A spec whose rules cannot compile must fail
		// here and not per transaction: a Rack that started has already told the
		// Mixer it is serving this protocol.
		spec, errS := sdl.ParseSemantic(specContent)
		if errS != nil {
			return fmt.Errorf("validation is %q but the spec cannot be read for it: %w", g.validation, errS)
		}
		v, errV := sdl.NewValidator(spec)
		if errV != nil {
			return fmt.Errorf("validation is %q but the spec's rules do not compile: %w", g.validation, errV)
		}
		g.validator = v
	default:
		return fmt.Errorf("unknown validation %q; use %q, %q or %q",
			g.validation, ValidationOff, ValidationWarn, ValidationEnforce)
	}

	// 3. Telemetry
	g.meter = otel.GetMeterProvider().Meter("fluxrig/gears/codec_iso8583")
	g.msgTotal, _ = g.meter.Int64Counter("flux.gear.messages_in", metric.WithDescription("Total messages processed by gear"))
	g.latency, _ = g.meter.Float64Histogram("flux.gear.processing_time_ms", metric.WithDescription("Gear processing latency"), metric.WithUnit("ms"))
	g.fieldsCount, _ = g.meter.Int64Histogram("flux.codec.iso8583.fields_count", metric.WithDescription("Number of fields processed per message"))
	g.errTotal, _ = g.meter.Int64Counter("flux.gear.errors", metric.WithDescription("Total gear errors"))
	g.violations, _ = g.meter.Int64Counter("flux.iso8583.violations",
		metric.WithDescription("Semantic rules broken by messages, by severity and kind"))

	g.logger.Info("Initialized ISO8583 Codec",
		"spec", specPath,
		"hash", meta.SpecHash,
		"spec_version", meta.SpecVersion,
		"direction", g.direction,
		"validation", g.validation,
	)

	return nil
}

// Start begins the active lifecycle of the Gear.
func (g *Gear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error {
	g.emit = emit
	g.logger.Info("starting gear",
		"flux.type", "GEAR",
		"flux.name", g.name,
		"type", "codec_iso8583",
	)
	return nil
}

// Process handles an incoming message (Filter Mode).
func (g *Gear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	start := time.Now()

	// 1. Determine direction
	dir := g.direction
	if dir == "auto" {
		if len(msg.RawPayload) > 0 {
			dir = "decode"
		} else {
			dir = "encode"
		}
	}

	var err error
	var fields []int
	var mti string

	if dir == "decode" {
		mti, fields, err = g.decode(msg)
	} else {
		mti, fields, err = g.encode(msg)
	}

	// The spec's own rules, applied to the message the codec just made sense of.
	// Only after a successful decode or encode: there is nothing to judge about
	// bytes that did not parse, and the parse error is the more useful one.
	if err == nil && g.validator != nil {
		err = g.applyRules(ctx, msg, mti, dir, gearAttrsFor(g.name, dir))
	}

	duration := time.Since(start)

	// 2. Telemetry
	status := "ok"
	gearAttrs := gearAttrsFor(g.name, dir)

	if err != nil {
		g.errTotal.Add(ctx, 1, metric.WithAttributes(append(gearAttrs,
			attribute.String("error_type", "processing"),
		)...))

		g.logger.Warn("Codec processing failed",
			"direction", dir,
			"error", err,
			"spec_hash", g.meta.SpecHash,
			"spec_version", g.meta.SpecVersion,
		)

		if g.onError == "drop" {
			return nil, nil // Filter out
		}
		return msg, err
	}

	// Success Telemetry
	msgAttrs := append(gearAttrs,
		attribute.String("mti", mti),
		attribute.String("status", status),
	)
	g.msgTotal.Add(ctx, 1, metric.WithAttributes(msgAttrs...))
	g.latency.Record(ctx, float64(duration.Milliseconds()), metric.WithAttributes(msgAttrs...))
	g.fieldsCount.Record(ctx, int64(len(fields)), metric.WithAttributes(gearAttrs...))

	// Persist metadata
	msg.Metadata["codec.spec_hash"] = g.meta.SpecHash
	// The hash proves which bytes; the id and version state which contract. A
	// trace carrying only the hash tells whoever reads it nothing they can act
	// on -- they cannot tell a comment change from a rule change, and they
	// cannot name the spec to whoever owns it.
	msg.Metadata["codec.spec_id"] = g.meta.SpecID
	msg.Metadata["codec.spec_version"] = g.meta.SpecVersion
	msg.Metadata["codec.protocol"] = g.meta.Protocol
	if mti != "" {
		msg.Metadata["iso8583.mti"] = mti
		if len(mti) >= mtiClassLen {
			// The first two digits are the version and the message class, which
			// a request and its reply share: 0100 and 0110 both yield "01",
			// while a reversal pair yields "04". The last two digits are the
			// function and origin, and those are exactly what differs between
			// the two, so the full MTI cannot correlate them.
			//
			// This exists so a correlation key can be scoped by message class.
			// Without it, an authorization and a reversal carrying the same
			// trace number collide in a correlation store.
			msg.Metadata["iso8583.mti_class"] = mti[:mtiClassLen]
		}
	}

	g.logger.Info("Codec message processed",
		"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
		"direction", dir,
		"mti", mti,
		"fields", fields,
		"duration_us", duration.Microseconds(),
		"spec_hash", g.meta.SpecHash,
		"spec_version", g.meta.SpecVersion,
	)

	// TRACE: Per-field dump
	if g.logger.Enabled(ctx, logger.LevelTrace) {
		g.traceFields(ctx, dir, mti, msg)
	}

	return msg, nil
}

// mtiClassLen is the number of leading MTI digits that a request and its reply
// have in common: the version and the message class.
const mtiClassLen = 2

func (g *Gear) decode(msg *fluxmsg.FluxMsg) (string, []int, error) {
	isoMsg := iso8583.NewMessage(g.moovSpec)
	if err := isoMsg.Unpack(msg.RawPayload); err != nil {
		return "", nil, fmt.Errorf("unpack failed: %w", err)
	}

	mti, _ := isoMsg.GetString(0)
	if len(mti) > 0 {
		for len(mti) < 4 {
			mti = "0" + mti
		}
	}
	presentFields := make([]int, 0)

	// Bitmap starts at field 1, but MTI is field 0
	for i := 0; i <= 128; i++ {
		if i > 0 && !isoMsg.Bitmap().IsSet(i) {
			continue
		}
		presentFields = append(presentFields, i)

		// Map to Data using Alias if available
		val, err := isoMsg.GetString(i)
		if err != nil {
			continue
		}

		raw, rawErr := isoMsg.GetBytes(i)
		stored := storable(val, raw, rawErr)

		alias, ok := g.meta.Aliases[i]
		if ok {
			_ = msg.Set(alias, stored)
		}
		// Always store in Data with field prefix for transparency/trace.
		key := fmt.Sprintf("iso8583.field.%d", i)
		_ = msg.Set(key, stored)

		// Subfield Extraction
		f := isoMsg.GetField(i)
		if comp, ok := f.(*field.Composite); ok {
			for subKey, subField := range comp.GetSubfields() {
				subVal, err := subField.String()
				if err != nil {
					continue
				}
				subRaw, subRawErr := subField.Bytes()
				subStored := storable(subVal, subRaw, subRawErr)

				// 1. Raw Subfield ID
				_ = msg.Set(fmt.Sprintf("iso8583.field.%d.%s", i, subKey), subStored)

				// 2. Alias for Subfield (if defined)
				if subAliases, hasSubAliases := g.meta.SubAliases[i]; hasSubAliases {
					if alias, hasAlias := subAliases[subKey]; hasAlias {
						_ = msg.Set(alias, subStored)
					}
				}
			}
		}
	}

	g.preserveUnknownTags(isoMsg, msg)

	return mti, presentFields, nil
}

// storable picks the representation a decoded value must take to survive the
// bus.
//
// A fluxMsg travels as CBOR, which encodes a Go string as a text string and so
// requires valid UTF-8. Binary payloads -- composites, PIN blocks, MACs, EMV
// tags -- are not text, and holding them in a string corrupts the message the
// first time it crosses a rack boundary: the receiving end rejects the whole
// message, not just the offending field.
//
// Every place a decoded value enters Data must go through here. A single path
// that skips it, an alias for instance, is enough to lose the message.
func storable(val string, raw []byte, rawErr error) any {
	if rawErr == nil && !utf8.ValidString(val) {
		return raw
	}
	return val
}

// unknownTagsKey holds TLV tags present on the wire but absent from the spec.
// They are kept so a re-encoded message still carries the brand-specific data
// it arrived with, which for a switch is the difference between forwarding a
// message and quietly truncating it.
const unknownTagsKey = "iso8583.unknown_tags"

// toBytes reports whether a Data value is a binary payload. CBOR decodes a byte
// string back into []byte, so a value that made the round trip across the bus
// still arrives as bytes rather than as text.
func toBytes(v any) ([]byte, bool) {
	switch b := v.(type) {
	case []byte:
		return b, true
	default:
		return nil, false
	}
}

// preserveUnknownTags copies retained unknown TLV tags into the fluxMsg.
//
// Values are stored as []byte on purpose. A fluxMsg crosses gear and rack
// boundaries as CBOR, which encodes a Go string as a text string and therefore
// requires valid UTF-8; an unknown TLV value is arbitrary binary and would fail
// to survive that round trip. Byte slices encode as CBOR byte strings and come
// back identical.
func (g *Gear) preserveUnknownTags(isoMsg *iso8583.Message, msg *fluxmsg.FluxMsg) {
	unknown := iso8583.UnknownTags(isoMsg)
	if len(unknown) == 0 {
		return
	}
	tags := make(map[string][]byte, len(unknown))
	for path, f := range unknown {
		raw, err := f.Bytes()
		if err != nil {
			continue
		}
		tags[path] = raw
	}
	if len(tags) > 0 {
		msg.Data[unknownTagsKey] = tags
	}
}

func (g *Gear) encode(msg *fluxmsg.FluxMsg) (string, []int, error) {
	isoMsg := iso8583.NewMessage(g.moovSpec)

	mti := msg.Metadata["iso8583.mti"]
	if mti != "" {
		_ = isoMsg.Field(0, mti)
	}

	presentFields := make([]int, 0)
	if mti != "" {
		presentFields = append(presentFields, 0)
	}

	// 1. Prioritize Aliases (allow user to override raw fields via business aliases)
	for id, alias := range g.meta.Aliases {
		if id == 0 {
			continue // MTI handled above
		}

		val, ok := msg.Get(alias)
		if !ok {
			continue
		}

		strVal := fmt.Sprintf("%v", val)
		if err := isoMsg.Field(id, strVal); err != nil {
			return mti, presentFields, fmt.Errorf("failed to set field %d (%s): %w", id, alias, err)
		}
		presentFields = append(presentFields, id)
	}

	// 2. Fill gaps with raw fields (iso8583.field.N) if not already set by alias
	for i := 2; i <= 128; i++ {
		if isoMsg.Bitmap().IsSet(i) {
			continue // Already set via alias
		}
		key := fmt.Sprintf("iso8583.field.%d", i)
		val, ok := msg.Get(key)
		if !ok {
			continue
		}
		// Binary payloads arrive as bytes (see decode) and must be set as such,
		// otherwise the value is reinterpreted as text and the field is
		// corrupted. This is also what carries unknown TLV tags back out: they
		// live inside the composite's raw bytes.
		if raw, isBytes := toBytes(val); isBytes {
			if err := isoMsg.BinaryField(i, raw); err != nil {
				return mti, presentFields, fmt.Errorf("failed to set raw field %d: %w", i, err)
			}
			presentFields = append(presentFields, i)
			continue
		}
		strVal := fmt.Sprintf("%v", val)
		if err := isoMsg.Field(i, strVal); err != nil {
			return mti, presentFields, fmt.Errorf("failed to set raw field %d: %w", i, err)
		}
		presentFields = append(presentFields, i)
	}

	packed, err := isoMsg.Pack()
	if err != nil {
		return mti, presentFields, fmt.Errorf("pack failed: %w", err)
	}

	msg.RawPayload = packed
	return mti, presentFields, nil
}

// traceFields emits a TRACE-level log for every field present in the message.
func (g *Gear) traceFields(ctx context.Context, dir, mti string, msg *fluxmsg.FluxMsg) {
	var fields []string

	// Show fields that are set in the message data
	// We walk 0-128 to show them in order
	for i := 0; i <= 128; i++ {
		key := fmt.Sprintf("iso8583.field.%d", i)
		val, ok := msg.Get(key)
		if !ok {
			// Try alias if not found in raw
			alias, hasAlias := g.meta.Aliases[i]
			if hasAlias {
				val, ok = msg.Get(alias)
			}
		}

		if ok {
			display := fmt.Sprintf("%v", val)
			if g.meta.SecureIDs[i] {
				display = maskValue(display)
			}
			label := ""
			if alias, hasAlias := g.meta.Aliases[i]; hasAlias {
				label = fmt.Sprintf("(%s)", alias)
			}
			fields = append(fields, fmt.Sprintf("F%03d%s=%s", i, label, display))
		}
	}

	g.logger.Log(ctx, logger.LevelTrace, "Codec field dump",
		"direction", dir,
		"mti", mti,
		"spec_hash", g.meta.SpecHash,
		"spec_version", g.meta.SpecVersion,
		"field_count", len(fields),
		"fields", strings.Join(fields, " | "),
	)
}

// maskValue masks all but the first 6 and last 4 characters.
func maskValue(s string) string {
	if len(s) <= 10 {
		return strings.Repeat("*", len(s))
	}
	return s[:6] + strings.Repeat("*", len(s)-10) + s[len(s)-4:]
}

// Drain signals the gear to stop accepting new input.
func (g *Gear) Drain(ctx context.Context) error {
	return nil
}

// Stop acts as the cleanup hook.
func (g *Gear) Stop() error {
	g.logger.Info("Stopping ISO8583 Codec Gear")
	return nil
}

// isSpecURN reports whether a spec reference names a store artefact rather than
// a file. The store's vocabulary is `name:tag` or a bare content hash, and
// neither can be confused with a path: a path to a spec has a separator or an
// extension and no colon.
func isSpecURN(ref string) bool {
	if strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml") {
		return false
	}
	return strings.Contains(ref, ":") || isHex(ref)
}

func isHex(s string) bool {
	if len(s) < 32 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// loadConfiguredSpec compiles the spec a scenario named. A path is read
// directly: reaching into the gear context for a store the reference does not
// need would make a plain file spec depend on plumbing it never uses.
// It returns the document as well as the compiled form. Anything built from the
// spec's semantic half -- the validator, and the generator after it -- needs the
// document, and a spec resolved from the store has no path to read it back from.
func loadConfiguredSpec(ctx sdk.GearContext, ref string) (*iso8583.MessageSpec, *sdl.FieldMeta, []byte, error) {
	// A path is read without touching the gear context at all. Reaching into it
	// for a store the reference does not need would make a plain file spec
	// depend on plumbing it never uses -- which it briefly did, and every test
	// with a context that has no store crashed.
	if !isSpecURN(ref) {
		return readSpecFile(ref)
	}
	return resolveSpec(ctx.Context(), ctx.Manager(), ref)
}

// readSpecFile loads a spec from disk, returning the document alongside the
// compiled form.
func readSpecFile(ref string) (*iso8583.MessageSpec, *sdl.FieldMeta, []byte, error) {
	content, err := os.ReadFile(filepath.Clean(ref))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read spec file: %w", err)
	}
	moovSpec, meta, err := sdl.LoadSpecContent(content, filepath.Dir(ref))
	return moovSpec, meta, content, err
}

// resolveSpec turns a scenario's spec reference into a compiled spec. A URN is
// resolved through the content-addressed store, which is what makes a spec a
// deployed artefact rather than a file the Rack was assumed to already hold: two
// Racks given the same reference compile the same bytes, and the store says so.
//
// A path still works, because a spec being developed is a file on disk and
// iterating on it should not require an import.
func resolveSpec(ctx context.Context, mgr manager.Manager, ref string) (*iso8583.MessageSpec, *sdl.FieldMeta, []byte, error) {
	if !isSpecURN(ref) {
		return readSpecFile(ref)
	}
	if mgr == nil {
		return nil, nil, nil, fmt.Errorf("spec %q names a store artefact, but this gear has no store to resolve it against", ref)
	}
	content, err := mgr.Load(ctx, ref)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve %q from the store: %w", ref, err)
	}
	moovSpec, meta, err := sdl.LoadSpecContent(content, "")
	return moovSpec, meta, content, err
}

// gearAttrsFor is the attribute set every measurement from this gear carries.
func gearAttrsFor(name, dir string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("gear_type", "iso8583.codec"),
		attribute.String("gear_name", name),
		attribute.String("direction", dir),
	}
}

// applyRules answers the message against the spec's semantic rules.
//
// Warnings are recorded and the message goes on; that is what makes a rule
// deployable to a live fleet before it is enforced on one. A rejection returns
// an error, so it takes the same path as a decode failure and honours on_error
// -- an operator who chose to drop bad messages does not get a different answer
// because this one was well formed and wrong rather than malformed.
func (g *Gear) applyRules(ctx context.Context, msg *fluxmsg.FluxMsg, mti, dir string, gearAttrs []attribute.KeyValue) error {
	violations := g.validator.Validate(subject{mti: mti, msg: msg})
	if len(violations) == 0 {
		return nil
	}

	enforcing := g.validation == ValidationEnforce
	for _, v := range violations {
		severity := v.Severity
		if !enforcing {
			// In warn mode nothing rejects, and the record says so rather than
			// reporting a rejection that did not happen.
			severity = sdl.SeverityWarn
		}
		g.violations.Add(ctx, 1, metric.WithAttributes(append(gearAttrs,
			attribute.String("severity", severity),
			attribute.String("kind", v.Kind),
			attribute.String("mti", mti),
		)...))
		g.logger.Warn("Message breaks a spec rule",
			"flux_id", fmt.Sprintf("0x%x", msg.FluxID),
			"direction", dir,
			"mti", mti,
			"severity", severity,
			"kind", v.Kind,
			"rule", v.String(),
			"spec_id", g.meta.SpecID,
			"spec_version", g.meta.SpecVersion,
		)
	}

	// What was found travels with the message, so a downstream gear can route on
	// it and an operator reading a trace sees it without the logs.
	msg.Metadata["codec.violations"] = fmt.Sprint(len(violations))

	if enforcing && sdl.Rejects(violations) {
		return fmt.Errorf("message does not satisfy spec %s %s: %s",
			g.meta.SpecID, g.meta.SpecVersion, firstRejection(violations))
	}
	return nil
}

// firstRejection names one violation for the error. All of them are logged; an
// error string that carried a dozen would be unreadable where it surfaces.
func firstRejection(vs []sdl.Violation) string {
	for _, v := range vs {
		if v.Severity == sdl.SeverityReject {
			return v.String()
		}
	}
	return vs[0].String()
}
