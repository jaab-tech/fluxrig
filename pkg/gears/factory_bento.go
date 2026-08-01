// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

//go:build !nobento

package gears

import (
	"github.com/jaab-tech/fluxrig/pkg/gears/native/bento"
	"github.com/jaab-tech/fluxrig/pkg/sdk"
)

// registerOptional wires the Bento gear into the factory.
//
// Bento is the single largest contributor to binary size: roughly 18 MB of the
// ~32 MB stripped Rack binary (~57%). It also pulls protobuf, cue, avro, gojq
// and others in transitively, none of which the rest of fluxrig uses. Builds
// that do not need Bento's mapping/local-I/O components can drop all of it with
// `-tags nobento`, producing a Rack of roughly 13 MB.
//
// This file is compiled unless the `nobento` tag is set, so the default build
// keeps the Bento gear and existing scenarios are unaffected.
func registerOptional(f *Factory) {
	f.Register("bento", func() sdk.NativeGear { return bento.New() })
}
