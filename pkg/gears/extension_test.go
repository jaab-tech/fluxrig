// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package gears

import (
	"slices"
	"testing"

	"github.com/jaab-tech/fluxrig/pkg/gears/native/coatcheck"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// isolateExtensions gives a test an empty extension list and restores the
// previous one afterwards, because the list is package state.
func isolateExtensions(t *testing.T) {
	t.Helper()
	extensionsMu.Lock()
	saved := extensions
	extensions = nil
	extensionsMu.Unlock()
	t.Cleanup(func() {
		extensionsMu.Lock()
		extensions = saved
		extensionsMu.Unlock()
	})
}

func TestNewFactoryWithoutExtensionsHasOnlyBuiltIns(t *testing.T) {
	isolateExtensions(t)

	types := NewFactory().Types()
	if !slices.Contains(types, "io_tcp") {
		t.Fatalf("built-in gear io_tcp is missing from %v", types)
	}
	if slices.Contains(types, "ext_probe") {
		t.Fatalf("a gear nobody registered showed up: %v", types)
	}
}

func TestNewFactoryAppliesRegisteredExtensions(t *testing.T) {
	isolateExtensions(t)

	RegisterExtension(func(f *Factory) {
		f.Register("ext_probe", func() sdk.NativeGear { return coatcheck.New() })
	})

	f := NewFactory()
	if _, err := f.Create("ext_probe"); err != nil {
		t.Fatalf("an extension gear cannot be created: %v", err)
	}
	if _, ok := f.Manifest("ext_probe"); !ok {
		t.Fatal("an extension gear has no manifest in the catalog")
	}
	if !slices.Contains(f.Types(), "io_tcp") {
		t.Fatal("registering an extension dropped a built-in gear")
	}
}

func TestExtensionsApplyInRegistrationOrder(t *testing.T) {
	isolateExtensions(t)

	var order []string
	RegisterExtension(func(*Factory) { order = append(order, "first") })
	RegisterExtension(func(*Factory) { order = append(order, "second") })

	NewFactory()

	if !slices.Equal(order, []string{"first", "second"}) {
		t.Fatalf("extensions ran in the order %v", order)
	}
}

func TestExtensionReplacesABuiltInOfTheSameType(t *testing.T) {
	isolateExtensions(t)

	replaced := false
	RegisterExtension(func(f *Factory) {
		f.Register("io_tcp", func() sdk.NativeGear {
			replaced = true
			return coatcheck.New()
		})
	})

	if _, err := NewFactory().Create("io_tcp"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !replaced {
		t.Fatal("the extension constructor did not replace the built-in one")
	}
}
