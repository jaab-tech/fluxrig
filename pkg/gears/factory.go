// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package gears

import (
	"fmt"
	"sort"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/coatcheck"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/conductor"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/io_tcp"
	iso8583codec "github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec"
	iso8583io "github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/io"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/wasm"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Factory instantiates gears by type.
type Factory struct {
	constructors map[string]func() sdk.NativeGear
	manifests    map[string]sdk.Manifest
}

func NewFactory() *Factory {
	f := &Factory{
		constructors: make(map[string]func() sdk.NativeGear),
		manifests:    make(map[string]sdk.Manifest),
	}
	// Register Built-ins
	f.Register("io_tcp", func() sdk.NativeGear { return &io_tcp.Gear{} })
	f.Register("io_iso8583", func() sdk.NativeGear { return &iso8583io.Gear{} })
	f.Register("codec_iso8583", func() sdk.NativeGear { return &iso8583codec.Gear{} })
	f.Register("coatcheck", func() sdk.NativeGear { return coatcheck.New() })
	f.Register("conductor", func() sdk.NativeGear { return conductor.New() })
	f.Register("wasm", func() sdk.NativeGear { return wasm.New() })

	// Optional gears, selected at build time. See factory_bento.go /
	// factory_nobento.go: a `nobento` build omits the Bento gear entirely.
	registerOptional(f)

	return f
}

// Types returns the gear type strings this binary can construct. It lets
// operators confirm which build variant they are running.
func (f *Factory) Types() []string {
	types := make([]string, 0, len(f.constructors))
	for t := range f.constructors {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

func (f *Factory) Register(typeStr string, ctor func() sdk.NativeGear) {
	f.constructors[typeStr] = ctor
	// Record the gear's manifest once at registration. Gears that publish one
	// (sdk.Manifested) give the real thing; the rest get a minimal manifest so
	// the catalog is complete and tooling has at least type/category.
	if m, ok := ctor().(sdk.Manifested); ok {
		man := m.Manifest()
		if man.Type == "" {
			man.Type = typeStr
		}
		f.manifests[typeStr] = man
	} else {
		f.manifests[typeStr] = sdk.Manifest{
			Type:     typeStr,
			Category: categoryForType(typeStr),
			Status:   sdk.StatusStable,
			Summary:  "No manifest declared yet.",
		}
	}
}

// categoryForType is a fallback classification for gears without a manifest,
// used only until every gear declares one (ADR 0045 Phase 2).
func categoryForType(typeStr string) sdk.GearCategory {
	switch {
	case len(typeStr) >= 3 && typeStr[:3] == "io_":
		return sdk.CategoryIO
	case len(typeStr) >= 5 && typeStr[:5] == "codec":
		return sdk.CategoryCodec
	default:
		return sdk.CategoryLogic
	}
}

// Manifest returns the manifest for a gear type.
func (f *Factory) Manifest(typeStr string) (sdk.Manifest, bool) {
	m, ok := f.manifests[typeStr]
	return m, ok
}

// Manifests returns every gear manifest this binary can construct, sorted by
// type.
func (f *Factory) Manifests() []sdk.Manifest {
	out := make([]sdk.Manifest, 0, len(f.manifests))
	for _, m := range f.manifests {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func (f *Factory) Create(typeStr string) (sdk.NativeGear, error) {
	ctor, ok := f.constructors[typeStr]
	if !ok {
		return nil, fmt.Errorf("unknown gear type: %s", typeStr)
	}
	return ctor(), nil
}
