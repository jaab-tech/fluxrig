// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package wasm

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
	"github.com/jaab-tech/fluxrig/pkg/wasm/catalog"
)

// Guest code for the fixture, in WebAssembly opcodes. CI has no Wasm toolchain,
// so the tests assemble their modules here instead of carrying a binary.
var (
	// allocBump returns the current heap top and moves it by len.
	allocBump = []byte{0x23, 0x00, 0x23, 0x00, 0x20, 0x00, 0x6a, 0x24, 0x00}
	// allocNull fails every allocation, as a guest allocator does when it is out of memory.
	allocNull = []byte{0x41, 0x00}

	// freeCountAndOverwrite adds one to the counter at address 0, then writes over
	// the first bytes of the block, as a free list does.
	freeCountAndOverwrite = []byte{
		0x41, 0x00, 0x41, 0x00, 0x28, 0x02, 0x00, 0x41, 0x01, 0x6a, 0x36, 0x02, 0x00,
		0x20, 0x00, 0x41, 0x7f, 0x36, 0x02, 0x00,
	}

	// processCopy writes the input to a new block and returns that block.
	processCopy = []byte{
		0x20, 0x01, 0x10, 0x00, 0x22, 0x02, 0x20, 0x00, 0x20, 0x01, 0xfc, 0x0a, 0x00, 0x00,
		0x20, 0x02, 0xad, 0x42, 0x20, 0x86, 0x20, 0x01, 0xad, 0x84,
	}
	// processInPlace returns the input block.
	processInPlace = []byte{0x20, 0x00, 0xad, 0x42, 0x20, 0x86, 0x20, 0x01, 0xad, 0x84}
	// processDrop drops the message.
	processDrop = []byte{0x42, 0x00}
	// processGrow asks for 1000 more pages of memory and drops the message.
	processGrow = []byte{0x41, 0xe8, 0x07, 0x40, 0x00, 0x1a, 0x42, 0x00}
)

const wasmPageLen = 65536

func uleb(v uint32) []byte {
	return binary.AppendUvarint(nil, uint64(v))
}

func vec(items ...[]byte) []byte {
	out := uleb(uint32(len(items)))
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}

func section(id byte, body []byte) []byte {
	return append(append([]byte{id}, uleb(uint32(len(body)))...), body...)
}

func export(name string, kind byte, index byte) []byte {
	return append(append(uleb(uint32(len(name))), name...), kind, index)
}

func funcBody(locals, code []byte) []byte {
	body := append(append(append([]byte{}, locals...), code...), 0x0b)
	return append(uleb(uint32(len(body))), body...)
}

// fixture assembles a module with the memory exports of the ABI.
func fixture(minPages uint32, alloc, process []byte) []byte {
	const (
		funcKind   = 0x00
		memoryKind = 0x02
	)
	noLocals := []byte{0x00}
	oneI32Local := []byte{0x01, 0x01, 0x7f}

	out := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}
	out = append(out, section(1, vec(
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f},       // (i32) -> i32
		[]byte{0x60, 0x02, 0x7f, 0x7f, 0x00},       // (i32, i32) -> ()
		[]byte{0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7e}, // (i32, i32) -> i64
	))...)
	out = append(out, section(3, vec([]byte{0x00}, []byte{0x01}, []byte{0x02}))...)
	out = append(out, section(5, vec(append([]byte{0x00}, uleb(minPages)...)))...)
	// One mutable i32 global: the heap top. It starts at 1024, after the free counter.
	out = append(out, section(6, vec([]byte{0x7f, 0x01, 0x41, 0x80, 0x10, 0x0b}))...)
	out = append(out, section(7, vec(
		export("memory", memoryKind, 0),
		export("alloc", funcKind, 0),
		export("free", funcKind, 1),
		export("process", funcKind, 2),
	))...)
	out = append(out, section(10, vec(
		funcBody(noLocals, alloc),
		funcBody(noLocals, freeCountAndOverwrite),
		funcBody(oneI32Local, process),
	))...)
	return out
}

func startGear(t *testing.T, module []byte, config map[string]any) *Gear {
	t.Helper()
	g, err := initGear(t, module, config)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop() })
	return g
}

func initGear(t *testing.T, module []byte, config map[string]any) (*Gear, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "module.wasm")
	if err := os.WriteFile(path, module, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"source": "file://" + path}
	for k, v := range config {
		cfg[k] = v
	}
	g := New()
	return g, g.Init(sdk.NewMockGearContext(cfg))
}

func testMsg() *fluxmsg.FluxMsg {
	return &fluxmsg.FluxMsg{
		FluxID:     uuid.New(),
		Metadata:   map[string]string{"conn.id": "c1"},
		Data:       map[string]any{"field": "value"},
		RawPayload: []byte("0800 raw bytes"),
	}
}

func freeCount(t *testing.T, g *Gear) uint32 {
	t.Helper()
	n, ok := g.mod.Memory().ReadUint32Le(0)
	if !ok {
		t.Fatal("read free counter")
	}
	return n
}

func TestProcess_DecodesReplyBeforeItIsFreed(t *testing.T) {
	g := startGear(t, fixture(1, allocBump, processCopy), nil)
	in := testMsg()

	out, err := g.Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if out == nil {
		t.Fatal("Process returned no message")
	}
	if out.FluxID != in.FluxID || string(out.RawPayload) != string(in.RawPayload) {
		t.Fatalf("reply = %+v, want the input back", out)
	}
	if got := freeCount(t, g); got != 2 {
		t.Fatalf("free calls = %d, want 2 (input and reply)", got)
	}
}

func TestProcess_DroppedMessageFreesInput(t *testing.T) {
	g := startGear(t, fixture(1, allocBump, processDrop), nil)

	out, err := g.Process(context.Background(), testMsg())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if out != nil {
		t.Fatalf("Process returned %+v, want a drop", out)
	}
	if got := freeCount(t, g); got != 1 {
		t.Fatalf("free calls = %d, want 1 (input)", got)
	}
}

func TestProcess_ReplyInInputBlockIsFreedOnce(t *testing.T) {
	g := startGear(t, fixture(1, allocBump, processInPlace), nil)
	in := testMsg()

	out, err := g.Process(context.Background(), in)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if out == nil || out.FluxID != in.FluxID {
		t.Fatalf("reply = %+v, want the input back", out)
	}
	if got := freeCount(t, g); got != 1 {
		t.Fatalf("free calls = %d, want 1 (one block)", got)
	}
}

func TestProcess_NullAllocationIsAnError(t *testing.T) {
	g := startGear(t, fixture(1, allocNull, processDrop), nil)

	if _, err := g.Process(context.Background(), testMsg()); err == nil {
		t.Fatal("Process accepted a null allocation")
	}
	if heap, ok := g.mod.Memory().Read(0, 4); !ok || binary.LittleEndian.Uint32(heap) != 0 {
		t.Fatalf("guest memory at address 0 changed: %x", heap)
	}
}

func TestInit_MemoryLimit(t *testing.T) {
	tests := []struct {
		name     string
		minPages uint32
		limit    any
		wantErr  bool
	}{
		{name: "default fits a module with a 1 MiB stack", minPages: 17},
		{name: "module over the limit", minPages: 17, limit: 16, wantErr: true},
		{name: "module at the limit", minPages: 17, limit: 17},
		{name: "limit over what Wasm addresses", minPages: 1, limit: 65537, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]any{}
			if tt.limit != nil {
				config["memory_limit_pages"] = tt.limit
			}
			g, err := initGear(t, fixture(tt.minPages, allocBump, processDrop), config)
			t.Cleanup(func() { _ = g.Stop() })
			if (err != nil) != tt.wantErr {
				t.Fatalf("Init error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestProcess_MemoryLimitStopsGrowth(t *testing.T) {
	const limit = 20
	g := startGear(t, fixture(1, allocBump, processGrow), map[string]any{"memory_limit_pages": limit})

	if _, err := g.Process(context.Background(), testMsg()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if size := g.mod.Memory().Size(); size > limit*wasmPageLen {
		t.Fatalf("memory = %d pages, want at most %d", size/wasmPageLen, limit)
	}
}

func TestVerifyPayload(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	module := fixture(1, allocBump, processDrop)
	sign := func(key ed25519.PrivateKey) []byte {
		hash := sha256.Sum256(module)
		signed, errSign := catalog.AppendCustomSection(module, "fluxrig.cluster.signature", ed25519.Sign(key, hash[:]))
		if errSign != nil {
			t.Fatal(errSign)
		}
		return signed
	}
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	const (
		fromCatalog = "snake://wasm_catalog/abc.wasm"
		fromFile    = "file:///tmp/module.wasm"
	)
	tests := []struct {
		name    string
		source  string
		key     []byte
		payload []byte
		wantErr bool
	}{
		{name: "catalog, signed", source: fromCatalog, key: pub, payload: sign(priv)},
		{name: "catalog, no signature", source: fromCatalog, key: pub, payload: module, wantErr: true},
		{name: "catalog, wrong signature", source: fromCatalog, key: pub, payload: sign(otherKey), wantErr: true},
		{name: "catalog, no key on the Rack", source: fromCatalog, payload: sign(priv), wantErr: true},
		{name: "file, signed", source: fromFile, key: pub, payload: sign(priv)},
		{name: "file, no signature", source: fromFile, key: pub, payload: module},
		{name: "file, wrong signature", source: fromFile, key: pub, payload: sign(otherKey), wantErr: true},
		{name: "file, no key on the Rack", source: fromFile, payload: module},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Gear{config: Config{Source: tt.source}, log: slog.Default()}
			got, err := g.verifyPayload(tt.key, tt.payload)
			if (err != nil) != tt.wantErr {
				t.Fatalf("verifyPayload error = %v, wantErr %t", err, tt.wantErr)
			}
			if err == nil && !bytes.Equal(got, module) {
				t.Fatalf("verifyPayload returned %d bytes, want the %d of the module", len(got), len(module))
			}
		})
	}
}
