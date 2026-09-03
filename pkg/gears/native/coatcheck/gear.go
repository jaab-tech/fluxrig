// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package coatcheck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"encoding/base64"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

const (
	ModeStore   = "store"
	ModeRestore = "restore"
	ModeDaemon  = "daemon"
)

// Key normalization modes.
//
// A correlation key only works when both sides of an exchange render the same
// logical value the same way. They often do not: a request and its reply can
// cross gears configured with different specs, and a field that is zero-padded
// on one side may arrive trimmed on the other. The join is exact, so "000123"
// and "123" are simply different keys, and the failure is silent -- nothing
// errors, the entry is never found, and whatever the correlation was for stops
// happening.
const (
	// NormalizeTrim removes surrounding whitespace. It is the default because
	// it can only ever make two renderings of one value agree, never make two
	// distinct values collide.
	NormalizeTrim = "trim"
	// NormalizeNumeric additionally drops leading zeros, so fixed-width numeric
	// fields agree regardless of the width each side declares. Opt-in: it makes
	// "0001" and "1" the same key, which is right for a padded counter and
	// wrong for an identifier where the padding is significant.
	NormalizeNumeric = "numeric"
	// NormalizeNone joins values exactly as they are rendered.
	NormalizeNone = "none"
)

// CoatCheckGear implements the Generic Context Correlation logic.
type CoatCheckGear struct {
	ctx    sdk.GearContext
	config *Config

	// Mode Implementations
	store   *StoreLogic
	restore *RestoreLogic
	daemon  *DaemonLogic

	emit func(*fluxmsg.FluxMsg)
}

type Config struct {
	Mode         string   `mapstructure:"mode"`
	Bucket       string   `mapstructure:"bucket"`
	KeyFields    []string `mapstructure:"key_fields"`
	KeyNormalize string   `mapstructure:"key_normalize"`

	// AwaitStore decides whether a stored message waits for the write.
	//
	// Defaults to true, because the original use of this gear is stripping a
	// field and reattaching it on the reply, where forwarding before the entry
	// exists produces a reply that can never be made whole. Set it false when
	// the entry only enriches a record and losing one is preferable to holding
	// the message.
	AwaitStore    bool          `mapstructure:"await_store"`
	StoreTimeout  time.Duration `mapstructure:"store_timeout"`
	ValueFields   []string      `mapstructure:"value_fields"`
	MergeStrategy string        `mapstructure:"merge_strategy"`
	OnMissing     string        `mapstructure:"on_missing"` // "error", "drop", "forward"

	// Daemon Config
	Storage       string        `mapstructure:"storage"`
	Replicas      int           `mapstructure:"replicas"`
	DefaultTTL    time.Duration `mapstructure:"default_ttl"`
	MaxTTL        time.Duration `mapstructure:"max_ttl"`
	IncludeValues bool          `mapstructure:"include_values"`
}

func New() sdk.NativeGear {
	return &CoatCheckGear{}
}

// extractKey generates the composite key from the FluxMsg data.
func (g *CoatCheckGear) extractKey(msg *fluxmsg.FluxMsg) (string, error) {
	if len(g.config.KeyFields) == 0 {
		return "", errors.New("no key_fields configured")
	}

	keyParts := make([]string, 0, len(g.config.KeyFields))
	for _, field := range g.config.KeyFields {
		val, found := sdk.GetValue(msg, field)
		if !found {
			return "", fmt.Errorf("missing key field: %s", field)
		}
		keyParts = append(keyParts, g.normalizeKeyPart(fmt.Sprint(val)))
	}
	// Sanitize Key for NATS KV (RawURLEncoding avoids padding =)
	rawKey := sdk.JoinKeys(keyParts...)
	return base64.RawURLEncoding.EncodeToString([]byte(rawKey)), nil
}

// normalizeKeyPart brings one rendering of a key field to a canonical form, so
// that the two sides of an exchange agree on the key even when they disagree on
// how to render the value.
func (g *CoatCheckGear) normalizeKeyPart(v string) string {
	switch g.config.KeyNormalize {
	case NormalizeNone:
		return v
	case NormalizeNumeric:
		trimmed := strings.TrimLeft(strings.TrimSpace(v), "0")
		if trimmed == "" {
			// An all-zero value is a value. Collapsing it to the empty string
			// would make it indistinguishable from a missing one.
			return "0"
		}
		return trimmed
	default:
		return strings.TrimSpace(v)
	}
}

func (g *CoatCheckGear) Init(ctx sdk.GearContext) error {
	g.ctx = ctx
	g.config = &Config{
		DefaultTTL:   1 * time.Minute,
		MaxTTL:       5 * time.Minute,
		KeyNormalize: NormalizeTrim,
		AwaitStore:   true,
		StoreTimeout: 5 * time.Second,
	}
	// Basic Config Loading (Manual map decoding for now, or use mapstructure if available in util)
	// Assuming raw map access for Phase 2 velocity
	cfg := ctx.Config()
	if v, ok := cfg["mode"].(string); ok {
		g.config.Mode = v
	}
	if v, ok := cfg["bucket"].(string); ok {
		g.config.Bucket = v
	}
	if v, ok := cfg["default_ttl"].(string); ok {
		if d, err := time.ParseDuration(v); err == nil {
			g.config.DefaultTTL = d
		}
	}
	if v, ok := cfg["max_ttl"].(string); ok {
		if d, err := time.ParseDuration(v); err == nil {
			g.config.MaxTTL = d
		}
	}
	if v, ok := cfg["on_missing"].(string); ok {
		g.config.OnMissing = v
	}
	if v, ok := cfg["merge_strategy"].(string); ok {
		g.config.MergeStrategy = v
	}
	if v, ok := cfg["key_normalize"].(string); ok {
		g.config.KeyNormalize = v
	}
	if v, ok := cfg["await_store"].(bool); ok {
		g.config.AwaitStore = v
	}
	if v, ok := cfg["store_timeout"].(string); ok {
		if d, err := time.ParseDuration(v); err == nil {
			g.config.StoreTimeout = d
		}
	}
	// Parse KeyFields (handle []interface{} from generic YAML/JSON)
	if v, ok := cfg["key_fields"].([]interface{}); ok {
		for _, item := range v {
			if s, ok := item.(string); ok {
				g.config.KeyFields = append(g.config.KeyFields, s)
			}
		}
	} else if v, ok := cfg["key_fields"].([]string); ok {
		g.config.KeyFields = v
	}

	// Parse ValueFields (Partial Storage)
	if v, ok := cfg["value_fields"].([]interface{}); ok {
		for _, item := range v {
			if s, ok := item.(string); ok {
				g.config.ValueFields = append(g.config.ValueFields, s)
			}
		}
	} else if v, ok := cfg["value_fields"].([]string); ok {
		g.config.ValueFields = v
	}

	// Daemon Config
	if v, ok := cfg["storage"].(string); ok {
		g.config.Storage = v
	}
	if v, ok := cfg["replicas"].(int); ok {
		g.config.Replicas = v
	}
	if v, ok := cfg["include_values"].(bool); ok {
		g.config.IncludeValues = v
	}

	if g.config.Bucket == "" {
		return errors.New("bucket is required")
	}

	switch g.config.Mode {
	case ModeStore:
		g.store = &StoreLogic{gear: g}
		// VALIDATION: Ensure Daemon exists?
		// We can't easily check other gears here during Init without global registry access.
		// We might rely on Factory or Runtime to validate scenarios.
	case ModeRestore:
		g.restore = &RestoreLogic{gear: g}
	case ModeDaemon:
		g.daemon = &DaemonLogic{gear: g}
		if err := g.daemon.Init(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid mode: %s", g.config.Mode)
	}

	return nil
}

func (g *CoatCheckGear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error {
	g.emit = emit
	if g.daemon != nil {
		return g.daemon.Start(ctx, emit)
	}
	return nil
}

func (g *CoatCheckGear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	switch g.config.Mode {
	case ModeStore:
		return g.store.Process(ctx, msg)
	case ModeRestore:
		return g.restore.Process(ctx, msg)
	default:
		// Daemon does not process input traffic usually
		return nil, nil
	}
}

func (g *CoatCheckGear) Drain(ctx context.Context) error {
	return nil
}

func (g *CoatCheckGear) Stop() error {
	if g.daemon != nil {
		g.daemon.Stop()
	}
	return nil
}
