// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

// PortBinding is the resolved terminus of one of a gear's output ports,
// derived by the runtime from the scenario wiring at activation. Routing
// gears use it to bind availability sensing to the right signal source
// without duplicating topology in their own configuration.
type PortBinding struct {
	// Kind classifies the terminus:
	//   "io"      the port reaches a local I/O gear (through transparent
	//             single-path gears such as codecs); watch that gear's
	//             link-state.
	//   "remote"  the port crosses the Rack boundary; liveness comes from
	//             peer signals, not a local socket.
	//   "unbound" no unambiguous terminus (a fork mid-path, or a leg that
	//             does not end in an I/O gear); only outcome-based sensing
	//             applies.
	Kind string

	// Gear is the local I/O gear name when Kind is "io".
	Gear string
}

const (
	BindingIO      = "io"
	BindingRemote  = "remote"
	BindingUnbound = "unbound"
)

// BindingsProvider is an optional capability of a GearContext: contexts
// created by the Rack runtime implement it, handing each gear the resolved
// terminus of its output ports. Gears must treat its absence (or an empty
// map) as "no binding information" and fall back to their default sensing.
type BindingsProvider interface {
	// Bindings maps this gear's output port names to their termini.
	Bindings() map[string]PortBinding
}
