// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

// Package wasm provides a wazero-powered WebAssembly runtime for fluxrig gears.
package wasm

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/fxamacker/cbor/v2"
	"github.com/mitchellh/mapstructure"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/wasm/catalog"
)

// Gear implements sdk.NativeGear using the wazero runtime.
type Gear struct {
	config Config
	ctx    sdk.GearContext
	log    *slog.Logger

	runtime wazero.Runtime
	mod     api.Module

	malloc  api.Function
	free    api.Function
	process api.Function

	mu sync.Mutex
}

// New creates a new uninitialized Wasm gear.
func New() *Gear {
	return &Gear{}
}

// Init initializes the Wazero runtime, fetches the payload, and links the host functions.
func (g *Gear) Init(ctx sdk.GearContext) error {
	g.ctx = ctx
	g.log = ctx.Logger()

	if err := mapstructure.Decode(ctx.Config(), &g.config); err != nil {
		return fmt.Errorf("failed to decode wasm config: %w", err)
	}
	g.config.ApplyDefaults()

	// 1. Fetch the payload
	payload, err := g.fetchPayload(ctx.Context())
	if err != nil {
		return fmt.Errorf("failed to fetch wasm payload: %w", err)
	}

	// 1b. Verify Wasm Payload
	payload, err = g.verifyPayload(ctx.ClusterPublicKey(), payload)
	if err != nil {
		return err
	}

	// 2. Initialize Wazero
	if g.config.MemoryLimitPages > maxMemoryLimitPages {
		return fmt.Errorf("wasm memory_limit_pages %d exceeds the maximum of %d", g.config.MemoryLimitPages, maxMemoryLimitPages)
	}
	g.runtime = wazero.NewRuntimeWithConfig(ctx.Context(),
		wazero.NewRuntimeConfig().WithMemoryLimitPages(g.config.MemoryLimitPages))

	// Register Host Functions (env.log)
	envBuilder := g.runtime.NewHostModuleBuilder("env")
	envBuilder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(g.hostLog), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).
		Export("log")

	_, err = envBuilder.Instantiate(ctx.Context())
	if err != nil {
		return fmt.Errorf("failed to instantiate host module: %w", err)
	}

	// Compile & Instantiate
	compiled, err := g.runtime.CompileModule(ctx.Context(), payload)
	if err != nil {
		return fmt.Errorf("failed to compile wasm module: %w", err)
	}

	modConfig := wazero.NewModuleConfig().WithName(ctx.GearName())
	g.mod, err = g.runtime.InstantiateModule(ctx.Context(), compiled, modConfig)
	if err != nil {
		return fmt.Errorf("failed to instantiate wasm module: %w", err)
	}

	// Lookup ABI Functions
	g.malloc = g.mod.ExportedFunction("alloc")
	if g.malloc == nil {
		g.malloc = g.mod.ExportedFunction("malloc")
	}
	g.free = g.mod.ExportedFunction("free")
	g.process = g.mod.ExportedFunction(g.config.Entrypoint)

	if g.malloc == nil || g.free == nil || g.process == nil {
		return fmt.Errorf("wasm module missing required ABI exports (alloc/malloc, free, %s)", g.config.Entrypoint)
	}

	return nil
}

// verifyPayload checks the cluster signature of a module and returns the module
// without it. A module from the catalog must carry a valid signature. A local
// file may have none, for development.
func (g *Gear) verifyPayload(clusterPub, payload []byte) ([]byte, error) {
	fromCatalog := strings.HasPrefix(g.config.Source, "snake://")
	if len(clusterPub) == 0 {
		if fromCatalog {
			return nil, fmt.Errorf("wasm supply chain security violation: no cluster public key to verify %s", g.config.Source)
		}
		return payload, nil
	}

	sig, stripped, errExt := catalog.ExtractCustomSection(payload, "fluxrig.cluster.signature")
	if errExt != nil {
		if fromCatalog {
			return nil, fmt.Errorf("wasm supply chain security violation: %s has no cluster signature", g.config.Source)
		}
		g.log.Warn("Wasm payload missing fluxrig.cluster.signature, running in dev/insecure mode")
		return payload, nil
	}

	hashBytes := sha256.Sum256(stripped)
	if !ed25519.Verify(clusterPub, hashBytes[:], sig) {
		return nil, fmt.Errorf("wasm supply chain security violation: invalid cluster signature")
	}
	g.log.Info("Wasm supply chain cluster signature verified successfully")
	return stripped, nil
}

// fetchPayload retrieves the compiled .wasm module.
func (g *Gear) fetchPayload(ctx context.Context) ([]byte, error) {
	src := g.config.Source
	if strings.HasPrefix(src, "file://") {
		path := strings.TrimPrefix(src, "file://")
		return os.ReadFile(path)
	}
	if strings.HasPrefix(src, "snake://") {
		// e.g. snake://wasm-gears/my_gear.wasm
		parts := strings.SplitN(strings.TrimPrefix(src, "snake://"), "/", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid snake URL format: %s", src)
		}
		bucket := parts[0]
		key := parts[1]

		value, _, err := g.ctx.Bus().KV().Get(ctx, bucket, key)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch from snake KV %s/%s: %w", bucket, key, err)
		}
		return value, nil
	}

	return nil, fmt.Errorf("unsupported wasm source: %s", src)
}

// hostLog implements the imported env.log for the guest.
func (g *Gear) hostLog(ctx context.Context, mod api.Module, stack []uint64) {
	level := int32(stack[0])
	ptr := uint32(stack[1])
	length := uint32(stack[2])

	if mem, ok := mod.Memory().Read(ptr, length); ok {
		msg := string(mem)
		switch level {
		case 1:
			g.log.Debug(msg)
		case 2:
			g.log.Info(msg)
		case 3:
			g.log.Warn(msg)
		case 4:
			g.log.Error(msg)
		default:
			g.log.Info(msg)
		}
	}
}

// Start executes any background tasks (unused for filters).
func (g *Gear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error {
	return nil
}

// Process marshals the fluxMsg, passes it to Wasm, and unmarshals the result.
func (g *Gear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 1. Marshal to CBOR
	b, err := cbor.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("wasm cbor marshal failed: %w", err)
	}

	// 2. Allocate in Wasm
	allocRes, err := g.malloc.Call(ctx, uint64(len(b)))
	if err != nil || len(allocRes) == 0 {
		return nil, fmt.Errorf("wasm alloc failed: %w", err)
	}
	ptr := uint32(allocRes[0])
	if ptr == 0 {
		return nil, fmt.Errorf("wasm alloc returned a null pointer for %d bytes", len(b))
	}
	defer func() { _, _ = g.free.Call(ctx, uint64(ptr), uint64(len(b))) }()

	// 3. Write to Wasm memory
	if !g.mod.Memory().Write(ptr, b) {
		return nil, fmt.Errorf("wasm memory write failed")
	}

	// 4. Call process
	procRes, err := g.process.Call(ctx, uint64(ptr), uint64(len(b)))
	if err != nil || len(procRes) == 0 {
		return nil, fmt.Errorf("wasm process call failed: %w", err)
	}

	// 5. Read result. The input block is freed on return, on every path.
	packed := procRes[0]
	retPtr := uint32(packed >> 32)
	retLen := uint32(packed & 0xFFFFFFFF)

	// If returned length is 0, the message is dropped
	if retLen == 0 {
		return nil, nil
	}

	retBytes, ok := g.mod.Memory().Read(retPtr, retLen)
	if !ok {
		return nil, fmt.Errorf("wasm memory read failed")
	}

	// 6. Unmarshal result. retBytes is a view of guest memory, so this comes
	// before free gives the block back to the guest.
	var outMsg fluxmsg.FluxMsg
	errDecode := cbor.Unmarshal(retBytes, &outMsg)

	// 7. Free the result, unless the guest returned the input block.
	if retPtr != ptr {
		_, _ = g.free.Call(ctx, uint64(retPtr), uint64(retLen))
	}

	if errDecode != nil {
		return nil, fmt.Errorf("wasm cbor unmarshal failed: %w", errDecode)
	}

	return &outMsg, nil
}

// Drain cleans up pending work before stopping.
func (g *Gear) Drain(ctx context.Context) error {
	return nil
}

// Stop closes the Wasm runtime.
func (g *Gear) Stop() error {
	if g.runtime != nil {
		return g.runtime.Close(context.Background())
	}
	return nil
}
