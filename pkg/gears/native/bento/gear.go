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

package bento

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	_ "github.com/warpstreamlabs/bento/public/components/io"   // File/Std I/O
	_ "github.com/warpstreamlabs/bento/public/components/pure" // Standard plugins
	"github.com/warpstreamlabs/bento/public/service"
)

// Gear implements the NativeGear interface for Bento.
type Gear struct {
	// ... type definitions same ...

	logger *slog.Logger
	params map[string]any

	// Runtime
	stream *service.Stream
	env    *service.Environment
	inChan chan *fluxmsg.FluxMsg
	emitFn func(*fluxmsg.FluxMsg)
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Config
	config Config

	// Config - Dynamic Plugin Names
	inputType  string
	outputType string
}

// New creates a new instance of the Bento gear.
func New() *Gear {
	return &Gear{
		inChan: make(chan *fluxmsg.FluxMsg, 100),
	}
}

// Init loads configuration and prepares the gear.
func (g *Gear) Init(ctx sdk.GearContext) error {
	g.logger = ctx.Logger().With("type", "bento")
	g.params = ctx.Config()

	// Use the Gear Name to uniquify the plugins in the Global Scope
	// Use the Gear Name to uniquify the plugins in the Global Scope
	safeName := strings.ReplaceAll(ctx.GearName(), "-", "_")
	safeName = strings.ToLower(safeName) // Ensure lowercase
	g.inputType = fmt.Sprintf("flux_in_%s", safeName)
	g.outputType = fmt.Sprintf("flux_out_%s", safeName)

	// 1. Parse Config
	var cfg Config
	// ...
	if b, ok := g.params["bento"].(map[string]any); ok {
		cfg.Bento = b
	} else {
		return fmt.Errorf("missing 'bento' configuration block")
	}
	if lvl, ok := g.params["log_level"].(string); ok {
		cfg.LogLevel = lvl
	}
	inputs := []string{}
	outputs := []string{}
	if p, ok := g.params["ports"].(map[string]any); ok {
		if ins, ok := p["inputs"].([]any); ok {
			for _, i := range ins {
				inputs = append(inputs, i.(string))
			}
		}
		if outs, ok := p["outputs"].([]any); ok {
			for _, o := range outs {
				outputs = append(outputs, o.(string))
			}
		}
	}
	g.config = cfg

	// 2. Use Global Environment to access standard plugins
	g.env = service.GlobalEnvironment()

	// 3. Register Unique Plugins
	specIn := service.NewConfigSpec().
		Summary("Reads messages from FluxRig internal bus.").
		Field(service.NewStringField("name").Default("default"))

	err := g.env.RegisterInput(g.inputType, specIn,
		func(conf *service.ParsedConfig, mgr *service.Resources) (service.Input, error) {
			return &fluxInput{g: g}, nil
		})
	if err != nil {
		// If double registered (restart?), warn but might fail.
		// Unique names per instance should avoid this unless same name reused.
		// Assuming GearName is unique per Rack execution.
		return err
	}

	specOut := service.NewConfigSpec().
		Summary("Writes messages to FluxRig internal bus.").
		Field(service.NewStringField("name").Default("out"))

	err = g.env.RegisterOutput(g.outputType, specOut,
		func(conf *service.ParsedConfig, mgr *service.Resources) (service.Output, int, error) {
			return &fluxOutput{g: g}, 1, nil
		})
	if err != nil {
		return err
	}

	// 4. Auto-Wiring Injection with DYNAMIC names
	if len(inputs) > 0 {
		if _, hasInput := cfg.Bento["input"]; !hasInput {
			cfg.Bento["input"] = map[string]any{
				g.inputType: map[string]any{"name": inputs[0]},
			}
		}
	}
	if len(outputs) > 0 {
		if _, hasOutput := cfg.Bento["output"]; !hasOutput {
			cfg.Bento["output"] = map[string]any{
				g.outputType: map[string]any{"name": outputs[0]},
			}
		}
	}

	// 4b. Register Metrics Bridge
	if errMetrics := RegisterMetrics(g.env); errMetrics != nil {
		g.logger.Warn("Failed to register fluxrig metrics in bento", "error", errMetrics)
		// Non-fatal?
	}

	// Default Buffer if missing (Bento requires one usually, or defaults?)
	if _, hasBuffer := cfg.Bento["buffer"]; !hasBuffer {
		cfg.Bento["buffer"] = map[string]any{
			"memory": map[string]any{},
		}
	}

	// 5. Strict Validation
	yamlBytes, err := MapToYaml(cfg.Bento)
	if err != nil {
		return fmt.Errorf("failed to marshal bento config: %w", err)
	}

	builder := g.env.NewStreamBuilder() // Parse YAML
	builder.SetLogger(g.logger)
	if errYAML := builder.SetYAML(string(yamlBytes)); errYAML != nil {
		return fmt.Errorf("failed to parse bento yaml: %w", errYAML)
	}

	return nil
}

func (g *Gear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error {
	g.ctx, g.cancel = context.WithCancel(ctx)
	g.emitFn = emit

	builder := g.env.NewStreamBuilder()
	builder.SetLogger(g.logger)
	yamlBytes, err := MapToYaml(g.config.Bento)
	if err != nil {
		return fmt.Errorf("failed to marshal bento config in start: %w", err)
	}
	if errYAML := builder.SetYAML(string(yamlBytes)); errYAML != nil {
		return fmt.Errorf("bento config invalid in start: %w", errYAML)
	}

	g.stream, err = builder.Build()
	if err != nil {
		return fmt.Errorf("failed to build bento stream: %w", err)
	}

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.logger.Info("Bento Stream Started", "input", g.inputType, "output", g.outputType)
		if err := g.stream.Run(g.ctx); err != nil && err != context.Canceled {
			g.logger.Error("bento stream exited with error", "error", err)
		} else {
			g.logger.Info("Bento Stream Stopped gracefully")
		}
	}()

	return nil
}

// Process handles incoming messages from FluxRig (for Filter Mode).
func (g *Gear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	// Determine if we should push to Bento.
	// If Bento is in "Processor Mode", it uses 'flux_in' which reads from inChan.
	// We assume if Process is called, it's wired to this gear.

	// We Must PUSH to inChan.
	// BUT 'Process' in SDK expects a return immediately?
	// The SDK interface supports "Filter" pattern where Process returns the result.
	// However, Bento is async pipeline.
	// We cannot map Process(msg) -> Bento -> return result reliably in one synchronous call
	// unless we use a request/response channel pair per message, which is slow.

	// Strategy for Bento Gear:
	// It ALWAYS acts as a "Source" that happens to read from "inChan".
	// When used as a Filter in Scenario, the SDK might call Process.
	// If we return nil, the SDK considers it "consumed".
	// The Bento Output ('flux_out') will call Emit later.
	// So we return nil, nil.

	select {
	case g.inChan <- msg:
		return nil, nil // Consumed, result will come via Emit
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *Gear) Stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	close(g.inChan)
	if g.stream != nil {
		_ = g.stream.Stop(g.ctx)
	}
	g.wg.Wait()
	return nil
}

// --- Plugins ---

type fluxInput struct {
	g *Gear
}

func (i *fluxInput) Connect(ctx context.Context) error { return nil }
func (i *fluxInput) Read(ctx context.Context) (*service.Message, service.AckFunc, error) {
	select {
	case msg, ok := <-i.g.inChan:
		if !ok {
			return nil, nil, service.ErrEndOfInput
		}
		bMsg := ToBentoMessage(msg)
		i.g.logger.Debug("Bento Input Read", "id", msg.FluxID, "payload", string(msg.RawPayload))
		return bMsg, func(ctx context.Context, err error) error { return nil }, nil
	case <-ctx.Done():
		return nil, nil, service.ErrEndOfInput
	}
}
func (i *fluxInput) Close(ctx context.Context) error { return nil }

type fluxOutput struct {
	g *Gear
}

func (o *fluxOutput) Connect(ctx context.Context) error { return nil }
func (o *fluxOutput) Write(ctx context.Context, msg *service.Message) error {
	fm, err := FromBentoMessage(msg)
	if err != nil {
		return err
	}

	if o.g.emitFn != nil {
		o.g.emitFn(fm)
		o.g.logger.Debug("Bento Output Emitted", "id", fm.FluxID)
	}
	return nil
}
func (o *fluxOutput) Close(ctx context.Context) error { return nil }
