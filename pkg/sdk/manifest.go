// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

// A gear Manifest is the single machine-readable description of a gear type:
// its identity, the ports it declares, its configuration schema, its
// documentation, and how the runtime binding walk should treat it. It lives
// next to the gear code so it cannot drift from behavior, and it is the source
// of truth that replaces scattered hard-coded tables (classification, doc
// links, the io-terminus set) and ad-hoc config schemas.
//
// Consumers: runtime config/wire validation, scenario visualization,
// generated docs, and the Scenario Studio.
type Manifest struct {
	// Type is the gear type string, matching factory registration
	// (e.g. "io_iso8583").
	Type string `json:"type"`

	// Category groups the gear for classification and styling.
	Category GearCategory `json:"category"`

	// Status is the maturity of the gear type.
	Status GearStatus `json:"status"`

	// Summary is a one-line description.
	Summary string `json:"summary"`

	// DocSlug is the reference-page slug on the public docs site; the full
	// URL is derived. Empty means no dedicated page.
	DocSlug string `json:"doc_slug,omitempty"`

	// Ports are the input/output ports the gear declares.
	Ports []Port `json:"ports"`

	// ConfigSchema is the gear's configuration contract as a JSON Schema
	// (draft-07) document. Empty means the gear takes no configuration.
	ConfigSchema string `json:"config_schema,omitempty"`

	// Terminus tells the runtime binding walk how to treat this gear when it
	// is the endpoint of a wire path. The zero value (TerminusTransparent)
	// means a single-path pass-through, e.g. a codec.
	Terminus TerminusKind `json:"terminus"`
}

// GearCategory classifies a gear by role.
type GearCategory string

const (
	CategoryIO            GearCategory = "io"
	CategoryCodec         GearCategory = "codec"
	CategoryLogic         GearCategory = "logic"
	CategoryObservability GearCategory = "observability"
)

// GearStatus is a gear type's maturity.
type GearStatus string

const (
	StatusStable  GearStatus = "stable"
	StatusRoadmap GearStatus = "roadmap"
)

// TerminusKind tells the binding walk how a gear behaves as a wire endpoint.
type TerminusKind string

const (
	// TerminusTransparent: a single-path pass-through (e.g. a codec); the
	// walk follows the gear's single output onward.
	TerminusTransparent TerminusKind = "transparent"
	// TerminusIO: a client-mode I/O terminus that owns one connection, so
	// gear-level link-state is connection state. This replaces the
	// hard-coded io-gear-type set in the runtime.
	TerminusIO TerminusKind = "io"
	// TerminusOpaque: a leg that ends here but is not a watchable I/O
	// terminus (e.g. a sink); only outcome-based sensing applies.
	TerminusOpaque TerminusKind = "opaque"
)

// PortDir is the direction of a declared port.
type PortDir string

const (
	PortIn  PortDir = "input"
	PortOut PortDir = "output"
)

// Port is one declared port of a gear.
type Port struct {
	// Name is the port name. It may be a concrete name ("in", "out",
	// "error") or a pattern for user-named ports ("out_<name>") when Dynamic
	// is true. Port names never contain dots (dots separate rack/gear/port in a
	// wire endpoint); roles use underscores.
	Name string `json:"name"`

	// Dir is the port direction.
	Dir PortDir `json:"dir"`

	// Role is a free-form role tag for tooling and documentation:
	// "request"/"reply"/"response"/"error" on a switch, "ingress"/"egress"
	// on an I/O gear (relative to the mesh), or "message" on a plain
	// transform/logic data port.
	Role string `json:"role,omitempty"`

	// Dynamic is true when instances are user-named and Name is a pattern
	// (e.g. "out_<name>", "out_response_<origin>").
	Dynamic bool `json:"dynamic,omitempty"`

	// Summary is a one-line description of the port.
	Summary string `json:"summary,omitempty"`
}

// Manifested is the optional capability of a gear that publishes a Manifest.
// The factory records it at registration; gears that do not implement it get a
// minimal synthesized manifest.
type Manifested interface {
	Manifest() Manifest
}

// DocsBaseURL is the public documentation base for gear reference pages.
const DocsBaseURL = "https://fluxrig.org/docs/reference/gears/"

// DocURL returns the full documentation URL for a manifest, or the catalog
// overview when no slug is set.
func (m Manifest) DocURL() string {
	if m.DocSlug == "" {
		return DocsBaseURL + "overview"
	}
	return DocsBaseURL + m.DocSlug
}
