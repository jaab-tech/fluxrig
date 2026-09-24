// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package gears

import "sync"

// Extension adds gears to a Factory.
//
// A module outside this repository uses one to ship gears of its own. Its
// binary calls RegisterExtension, usually from an init function, and
// NewFactory then applies every registered extension after the built-in
// gears. The engine never imports such a module: the dependency points from
// the extension to the engine, so this repository builds without it.
//
// An extension that registers a type the factory already knows replaces that
// gear, because Register keeps the last constructor it was given.
type Extension func(*Factory)

var (
	extensionsMu sync.RWMutex
	extensions   []Extension
)

// RegisterExtension makes every Factory built from now on apply e. It is safe
// to call from several goroutines. A binary calls it before it builds its
// first Factory.
func RegisterExtension(e Extension) {
	extensionsMu.Lock()
	defer extensionsMu.Unlock()
	extensions = append(extensions, e)
}

// applyExtensions runs the registered extensions in registration order.
func applyExtensions(f *Factory) {
	extensionsMu.RLock()
	defer extensionsMu.RUnlock()
	for _, e := range extensions {
		e(f)
	}
}
