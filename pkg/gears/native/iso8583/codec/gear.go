package codec

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec/sdl"
	"github.com/jaab-tech/fluxrig/pkg/logger"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/moov-io/iso8583"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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
	g.msgTotal, _ = g.meter.Int64Counter("fluxrig_codec_messages_total", metric.WithDescription("Total messages processed by codec"))
	g.latency, _ = g.meter.Float64Histogram("fluxrig_codec_duration_seconds", metric.WithDescription("Codec processing latency"), metric.WithUnit("s"))
	g.fieldsCount, _ = g.meter.Int64Histogram("fluxrig_codec_fields_count", metric.WithDescription("Number of fields processed per message"))
	g.errTotal, _ = g.meter.Int64Counter("fluxrig_codec_errors_total", metric.WithDescription("Total codec errors"))

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
	if err != nil {
		g.errTotal.Add(ctx, 1, metric.WithAttributes(
			attribute.String("direction", dir),
			attribute.String("error_type", "processing"),
		))

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
	g.msgTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("direction", dir),
		attribute.String("mti", mti),
		attribute.String("status", status),
	))
	g.latency.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String("direction", dir),
		attribute.String("mti", mti),
	))
	g.fieldsCount.Record(ctx, int64(len(fields)), metric.WithAttributes(
		attribute.String("direction", dir),
	))

	// Persist metadata
	msg.Metadata["codec.spec_hash"] = g.meta.SpecHash
	msg.Metadata["codec.protocol"] = g.meta.Protocol
	if mti != "" {
		msg.Metadata["iso8583.mti"] = mti
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

	if g.emit != nil {
		g.emit(msg)
	}

	return msg, nil
}

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

		alias, ok := g.meta.Aliases[i]
		if ok {
			_ = msg.Set(alias, val)
		}
		// Always store in Data with field prefix for transparency/trace
		_ = msg.Set(fmt.Sprintf("iso8583.field.%d", i), val)

		// Subfield Extraction
		f := isoMsg.GetField(i)
		if comp, ok := f.(*sdl.CompositeField); ok {
			for subKey, subVal := range comp.GetSubvalues() {
				// 1. Raw Subfield ID
				_ = msg.Set(fmt.Sprintf("iso8583.field.%d.%s", i, subKey), subVal)

				// 2. Alias for Subfield (if defined)
				if subAliases, hasSubAliases := g.meta.SubAliases[i]; hasSubAliases {
					if alias, hasAlias := subAliases[subKey]; hasAlias {
						_ = msg.Set(alias, subVal)
					}
				}
			}
		}
	}

	return mti, presentFields, nil
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
