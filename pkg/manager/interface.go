// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"context"
	"time"
)

// Manager handles the lifecycle of Specs and Scenarios
type Manager interface {
	// Import snapshots a local spec file into the CAS and assigns it a name:tag.
	// Returns hash, finalName, finalTag, error.
	Import(ctx context.Context, filePath, name, tag string) (hash string, finalName string, finalTag string, err error)

	// ImportContent stores a spec that is already in hand — one that arrived over
	// the bus rather than being read from disk. Same rules as Import: the
	// document's declared version becomes the tag, and a tag that already names
	// different content is refused.
	ImportContent(ctx context.Context, content []byte, name, tag string) (hash string, finalName string, finalTag string, err error)

	// ImportScenario snapshots a local scenario file into the CAS and assigns it a name:tag.
	// Returns hash, finalName, finalTag, error.
	ImportScenario(ctx context.Context, filePath, name, tag string) (hash string, finalName string, finalTag string, err error)

	// Load retrieves the content of a spec/scenario by URN (e.g. "visa:latest" or "sha256:...")
	Load(ctx context.Context, urn string) (content []byte, err error)

	// Export resolves a URN and writes its content to the given output path.
	Export(ctx context.Context, urn, outputPath string) error

	// List returns all known artifacts, of every kind. Callers that serve one
	// kind must filter: the spec listing showed scenarios as specs for as long
	// as both were in one index and nothing said which was which.
	List(ctx context.Context) ([]ArtifactInfo, error)

	// History returns every version of one artifact, newest version first.
	// Ordering is by semver, not by import time: a patch to an old branch is
	// imported after a newer release and is not newer than it.
	History(ctx context.Context, kind Kind, name string) ([]ArtifactInfo, error)
}

// Kind is what an artifact is. The store holds specs and scenarios in one index
// and they are not interchangeable.
type Kind string

const (
	KindSpec     Kind = "spec"
	KindScenario Kind = "scenario"
)

// ArtifactInfo is one version of one artifact, with what the store knows about
// it. The attributes are what the store recorded, not what can be inferred: an
// artifact imported before the store recorded a date has none, and saying so is
// more use than a file timestamp presented as an import date.
type ArtifactInfo struct {
	Kind Kind   `json:"kind"`
	Name string `json:"name"`
	Tag  string `json:"tag"`
	Hash string `json:"hash"`

	// ImportedAt is zero for anything imported before the store kept dates.
	ImportedAt time.Time `json:"imported_at,omitzero"`
	// Size of the stored document in bytes; zero when unrecorded.
	Size int64 `json:"size,omitempty"`
	// Title is the human name the document declares, which is not the reference
	// it is filed under: "ISO 8583:1987 (ASCII)" is filed as iso8583-v87-ascii.
	Title string `json:"title,omitempty"`
	// Protocol is what a spec speaks. Empty for a scenario.
	Protocol string `json:"protocol,omitempty"`
	// Latest reports whether this version is the one `latest` resolves to.
	Latest bool `json:"latest,omitempty"`
}
