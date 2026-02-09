// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package coatcheck

import (
	"context"
	"errors"
	"fmt"
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
	Mode          string   `mapstructure:"mode"`
	Bucket        string   `mapstructure:"bucket"`
	KeyFields     []string `mapstructure:"key_fields"`
	ValueFields   []string `mapstructure:"value_fields"`
	MergeStrategy string   `mapstructure:"merge_strategy"`
	OnMissing     string   `mapstructure:"on_missing"` // "error", "drop", "forward"

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
		keyParts = append(keyParts, fmt.Sprint(val))
	}
	// Sanitize Key for NATS KV (RawURLEncoding avoids padding =)
	rawKey := sdk.JoinKeys(keyParts...)
	return base64.RawURLEncoding.EncodeToString([]byte(rawKey)), nil
}

func (g *CoatCheckGear) Init(ctx sdk.GearContext) error {
	g.ctx = ctx
	g.config = &Config{
		DefaultTTL: 1 * time.Minute,
		MaxTTL:     5 * time.Minute,
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
