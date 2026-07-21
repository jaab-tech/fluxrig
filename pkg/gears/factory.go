// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package gears

import (
	"fmt"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/bento"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/coatcheck"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/io_tcp"
	iso8583codec "github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/codec"
	iso8583io "github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/io"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/wasm"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// Factory instantiates gears by type.
type Factory struct {
	constructors map[string]func() sdk.NativeGear
}

func NewFactory() *Factory {
	f := &Factory{
		constructors: make(map[string]func() sdk.NativeGear),
	}
	// Register Built-ins
	f.Register("io_tcp", func() sdk.NativeGear { return &io_tcp.Gear{} })
	f.Register("bento", func() sdk.NativeGear { return bento.New() })
	f.Register("io_iso8583", func() sdk.NativeGear { return &iso8583io.Gear{} })
	f.Register("codec_iso8583", func() sdk.NativeGear { return &iso8583codec.Gear{} })
	f.Register("coatcheck", func() sdk.NativeGear { return coatcheck.New() })
	f.Register("wasm", func() sdk.NativeGear { return wasm.New() })

	return f
}

func (f *Factory) Register(typeStr string, ctor func() sdk.NativeGear) {
	f.constructors[typeStr] = ctor
}

func (f *Factory) Create(typeStr string) (sdk.NativeGear, error) {
	ctor, ok := f.constructors[typeStr]
	if !ok {
		return nil, fmt.Errorf("unknown gear type: %s", typeStr)
	}
	return ctor(), nil
}
