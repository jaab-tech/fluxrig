package gears

import (
	"fmt"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/bento"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/simple_tcp"
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
	f.Register("simple_tcp", func() sdk.NativeGear { return &simple_tcp.Gear{} })
	f.Register("bento", func() sdk.NativeGear { return bento.New() })
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
