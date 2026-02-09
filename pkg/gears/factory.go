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

package gears

import (
	"fmt"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/bento"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/coatcheck"
	"github.com/jaab-tech/fluxrig/pkg/gears/native/io_tcp"
	iso8583io "github.com/jaab-tech/fluxrig/pkg/gears/native/iso8583/io"
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
	f.Register("coatcheck", func() sdk.NativeGear { return coatcheck.New() })

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
