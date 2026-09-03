// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package codec

import (
	"context"
	"fmt"
	"log/slog"
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
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Config holds the configuration for the ISO8583 Codec gear.
type Config struct {
	Name      string `json:"name"`
	SpecPath  string `json:"spec_path"`
	Direction string `json:"direction"`
	OnError   string `json:"on_error"`
}

// Gear implements the NativeGear interface for the ISO8583 Codec.
type Gear struct {
	logger *slog.Logger
	name   string
	emit   func(*fluxmsg.FluxMsg)

	moovSpec  *iso8583.MessageSpec
	meta      *sdl.FieldMeta
	direction string // "auto", "encode", "decode"
	onError   string // "drop", "reject", "kill"

	// Telemetry
	meter       metric.Meter
	msgTotal    metric.Int64Counter
	latency     metric.Float64Histogram
	fieldsCount metric.Int64Histogram
	errTotal    metric.Int64Counter
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

	moovSpec, meta, err := sdl.LoadSpec(specPath)
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

	// 3. Telemetry
	g.meter = otel.GetMeterProvider().Meter("fluxrig/gears/codec_iso8583")
	g.msgTotal, _ = g.meter.Int64Counter("flux.gear.messages_in", metric.WithDescription("Total messages processed by gear"))
	g.latency, _ = g.meter.Float64Histogram("flux.gear.processing_time_ms", metric.WithDescription("Gear processing latency"), metric.WithUnit("ms"))
	g.fieldsCount, _ = g.meter.Int64Histogram("flux.codec.iso8583.fields_count", metric.WithDescription("Number of fields processed per message"))
	g.errTotal, _ = g.meter.Int64Counter("flux.gear.errors", metric.WithDescription("Total gear errors"))

	g.logger.Info("Initialized ISO8583 Codec",
		"spec", specPath,
		"hash", meta.SpecHash,
		"direction", g.direction,
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

	duration := time.Since(start)

	// 2. Telemetry
	status := "ok"
	gearAttrs := []attribute.KeyValue{
		attribute.String("gear_type", "iso8583.codec"),
		attribute.String("gear_name", g.name),
		attribute.String("direction", dir),
	}

	if err != nil {
		g.errTotal.Add(ctx, 1, metric.WithAttributes(append(gearAttrs,
			attribute.String("error_type", "processing"),
		)...))

		g.logger.Warn("Codec processing failed",
			"direction", dir,
			"error", err,
			"spec_hash", g.meta.SpecHash,
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
