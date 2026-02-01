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
	"context"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

type MockGear struct{}

func (m *MockGear) Init(ctx sdk.GearContext) error                               { return nil }
func (m *MockGear) Start(ctx context.Context, emit func(*fluxmsg.FluxMsg)) error { return nil }
func (m *MockGear) Process(ctx context.Context, msg *fluxmsg.FluxMsg) (*fluxmsg.FluxMsg, error) {
	return msg, nil
}
func (m *MockGear) Stop() error {
	return nil
}

func (m *MockGear) Drain(ctx context.Context) error {
	return nil
}

func TestFactory(t *testing.T) {
	f := NewFactory()

	// 1. Built-in
	g, err := f.Create("simple_tcp")
	if err != nil {
		t.Errorf("Create(simple_tcp) failed: %v", err)
	}
	if g == nil {
		t.Error("Returned nil gear")
	}

	// 2. Custom Registration
	f.Register("mock", func() sdk.NativeGear { return &MockGear{} })
	m, err := f.Create("mock")
	if err != nil {
		t.Errorf("Create(mock) failed: %v", err)
	}
	if _, ok := m.(*MockGear); !ok {
		t.Error("Did not return MockGear")
	}

	// 3. Unknown
	_, err = f.Create("unknown_gear_type")
	if err == nil {
		t.Error("Expected error for unknown type")
	}
}
