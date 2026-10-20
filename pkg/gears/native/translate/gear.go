// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package translate is the translate gear. It moves values from the aliases of
// one protocol spec to the aliases of another, as a translation spec says. It
// adds the target fields and removes none, and it sets the identity of the
// target message so that the codec after it encodes the right message.
package translate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/translation"
)

// Validation modes, with the same meaning as on the codecs.
const (
	ValidationOff     = "off"
	ValidationWarn    = "warn"
	ValidationEnforce = "enforce"
)

const (
	translationAuto = "auto"
	metaViolations  = "translate.violations"
	portError       = "error"

	// Values of error.reason on a message the gear rejects.
	ReasonViolation = "violation"          // a finding, with validation: enforce
	ReasonFailed    = "translation_failed" // the translation could not run
)

// Gear is the translate gear.
type Gear struct {
	logger *slog.Logger
	name   string

	emitter     sdk.PortEmitter
	spec        *translation.Spec
	translation string // "auto" or the name of one translation
	validation  string
	onError     string // "drop", "reject", "kill"

	msgTotal   metric.Int64Counter
	latency    metric.Float64Histogram
	errTotal   metric.Int64Counter
	violations metric.Int64Counter
}

var _ sdk.NativeGear = (*Gear)(nil)

// Init reads the configuration, loads the translation spec and the protocol
// specs it names, and reads the lookup records. Every reference (the spec, the
// protocol specs and the lookup sources) is a file path or a store artefact.
func (g *Gear) Init(ctx sdk.GearContext) error {
	g.name = ctx.GearName()
	g.logger = ctx.Logger().With("type", "translate")
	g.emitter = ctx.Emitter()
	cfg := ctx.Config()

	ref, _ := cfg["spec_path"].(string)
	if ref == "" {
		return fmt.Errorf("translate: missing 'spec_path' in configuration")
	}
	resolve := func(r string) ([]byte, string, error) { return load(ctx, r) }
	content, _, err := resolve(ref)
	if err != nil {
		return fmt.Errorf("translate: spec %s: %w", ref, err)
	}
	s, err := translation.Load(content, resolve)
	if err != nil {
		return fmt.Errorf("translate: spec %s: %w", ref, err)
	}
	if err := s.LoadLookups(resolve); err != nil {
		return fmt.Errorf("translate: spec %s: %w", ref, err)
	}
	g.spec = s

	g.translation = stringOr(cfg, "translation", translationAuto)
	if g.translation != translationAuto {
		if _, ok := s.Translations[g.translation]; !ok {
			return fmt.Errorf("translate: translation %q is not in spec %s", g.translation, s.ID)
		}
	}
	g.validation = stringOr(cfg, "validation", ValidationOff)
	switch g.validation {
	case ValidationOff, ValidationWarn, ValidationEnforce:
	default:
		return fmt.Errorf("translate: validation %q: use off, warn or enforce", g.validation)
	}
	g.onError = stringOr(cfg, "on_error", "drop")
	switch g.onError {
	case "drop", "reject", "kill":
	default:
		return fmt.Errorf("translate: on_error %q: use drop, reject or kill", g.onError)
	}

	meter := otel.GetMeterProvider().Meter("fluxrig/gears/translate")
	g.msgTotal, _ = meter.Int64Counter("flux.gear.messages_in", metric.WithDescription("Total messages processed by gear"))
	g.latency, _ = meter.Float64Histogram("flux.gear.processing_time_ms", metric.WithDescription("Gear processing latency"), metric.WithUnit("ms"))
	g.errTotal, _ = meter.Int64Counter("flux.gear.errors", metric.WithDescription("Total gear errors"))
	g.violations, _ = meter.Int64Counter("flux.translate.violations", metric.WithDescription("Translated messages that miss a required field, or that another spec version decoded"))

	protocols := make([]string, 0, len(s.Protocols))
	for name, p := range s.Protocols {
		protocols = append(protocols, name+"="+p.ID()+"@"+p.Version())
	}
	g.logger.Info("Initialized translate",
		"spec", ref, "spec_id", s.ID, "spec_version", s.Version, "hash", s.Hash,
		"protocols", protocols, "lookups", s.LookupSources(),
		"translation", g.translation, "validation", g.validation)
	return nil
}

// Start has nothing to start: the gear is a filter.
func (g *Gear) Start(context.Context, func(*fluxmsg.FluxMsg)) error { return nil }

// Drain has nothing to drain.
func (g *Gear) Drain(context.Context) error { return nil }

// Stop has nothing to stop.
func (g *Gear) Stop() error { return nil }

// Process translates one message.
func (g *Gear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	start := time.Now()
	if msg.Metadata == nil {
		msg.Metadata = map[string]string{}
	}
	if msg.Data == nil {
		msg.Data = map[string]any{}
	}
	var (
		name     = g.translation
		findings []string
	)
	t, err := g.spec.Select(g.translation, msg)
	if err == nil {
		name = t.Name
		findings, err = t.Apply(msg)
	}
	reason := ReasonFailed
	attrs := []attribute.KeyValue{attribute.String("gear", g.name), attribute.String("translation", name)}
	if err == nil && g.validation != ValidationOff && len(findings) > 0 {
		reason = ReasonViolation
		g.violations.Add(ctx, int64(len(findings)), metric.WithAttributes(attrs...))
		msg.Metadata[metaViolations] = strings.Join(findings, "; ")
		g.logger.Warn("Translated message breaks rules", "translation", name, "violations", findings, "validation", g.validation)
		if g.validation == ValidationEnforce {
			err = fmt.Errorf("translate: %s breaks %d rule(s): %s", name, len(findings), strings.Join(findings, "; "))
		}
	}
	if err != nil {
		g.errTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
		g.logger.Warn("Translation failed", "translation", name, "error", err, "spec_id", g.spec.ID, "spec_version", g.spec.Version)
		switch g.onError {
		case "drop":
			return nil, nil
		case "reject":
			// The message leaves on the error port, so the scenario can answer
			// it: a decline to the terminal rather than a timeout.
			msg.Metadata["error.reason"] = reason
			msg.Metadata["error.gear"] = g.name
			msg.Metadata["error.message"] = err.Error()
			if errEmit := g.emitter.Emit(portError, msg); errEmit != nil {
				g.logger.Warn("Rejected message not emitted; is the error port wired?", "translation", name, "error", errEmit)
			}
			return nil, nil
		}
		return msg, err
	}
	g.msgTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	g.latency.Record(ctx, float64(time.Since(start).Microseconds())/1000, metric.WithAttributes(attrs...))
	return msg, nil
}

func stringOr(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}

// load reads a reference: a file path, relative to the working directory, or
// a store artefact (name:tag or a content hash). It returns the
// directory that relative references inside the spec resolve against.
func load(ctx sdk.GearContext, ref string) ([]byte, string, error) {
	if !isStoreRef(ref) {
		return translation.FileResolver(filepath.Clean(ref))
	}
	mgr := ctx.Manager()
	if mgr == nil {
		return nil, "", fmt.Errorf("%q names a store artefact, but this gear has no store to resolve it against", ref)
	}
	content, err := mgr.Load(ctx.Context(), ref)
	if err != nil {
		return nil, "", fmt.Errorf("resolve %q from the store: %w", ref, err)
	}
	return content, "", nil
}

// isStoreRef reports whether a reference names a store artefact
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
