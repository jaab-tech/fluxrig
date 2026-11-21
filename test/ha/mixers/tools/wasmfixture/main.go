// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0
//
// wasmfixture writes what the catalog scenario imports: a minimal module with
// the exports of the gear ABI (module.wasm), the same module signed by a new
// vendor key (signed.wasm), and that key's public half (vendor.pub, 32 bytes).
// The module is assembled from opcodes, as the gear's tests do, since the
// testbed has no Wasm toolchain.
//
//	go run ./test/ha/mixers/tools/wasmfixture -out results/wasm
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jaab-tech/fluxrig/pkg/wasm/catalog"
)

func main() {
	out := flag.String("out", "", "directory to write the files to")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "wasmfixture: -out is required")
		os.Exit(2)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fail(err)
	}
	module := minimalModule()
	signed, err := catalog.AppendCustomSection(module, "fluxrig.signature", ed25519.Sign(priv, catalog.HashWasm(module)))
	if err != nil {
		fail(err)
	}
	if err = os.MkdirAll(*out, 0o750); err != nil {
		fail(err)
	}
	for name, data := range map[string][]byte{"module.wasm": module, "signed.wasm": signed, "vendor.pub": pub} {
		if err = os.WriteFile(filepath.Join(*out, name), data, 0o600); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "wasmfixture:", err)
	os.Exit(1)
}

// minimalModule exports memory, alloc (a bump allocator), free and process
// (which drops the message).
func minimalModule() []byte {
	allocBump := []byte{0x23, 0x00, 0x23, 0x00, 0x20, 0x00, 0x6a, 0x24, 0x00}
	freeNothing := []byte{}
	processDrop := []byte{0x42, 0x00}
	noLocals := []byte{0x00}
	out := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}
	out = append(out, section(1, vec(
		[]byte{0x60, 0x01, 0x7f, 0x01, 0x7f},       // (i32) -> i32
		[]byte{0x60, 0x02, 0x7f, 0x7f, 0x00},       // (i32, i32) -> ()
		[]byte{0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7e}, // (i32, i32) -> i64
	))...)
	out = append(out, section(3, vec([]byte{0x00}, []byte{0x01}, []byte{0x02}))...)
	out = append(out, section(5, vec([]byte{0x00, 0x01}))...)
	out = append(out, section(6, vec([]byte{0x7f, 0x01, 0x41, 0x80, 0x10, 0x0b}))...)
	out = append(out, section(7, vec(
		export("memory", 0x02, 0),
		export("alloc", 0x00, 0),
		export("free", 0x00, 1),
		export("process", 0x00, 2),
	))...)
	out = append(out, section(10, vec(
		funcBody(noLocals, allocBump),
		funcBody(noLocals, freeNothing),
		funcBody(noLocals, processDrop),
	))...)
	return out
}

func uleb(v uint32) []byte { return binary.AppendUvarint(nil, uint64(v)) }

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
