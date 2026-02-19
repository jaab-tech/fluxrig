// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"context"
)

// Manager handles the lifecycle of Specs and Scenarios
type Manager interface {
	// Import snapshots a local spec file into the CAS and assigns it a name:tag.
	// Returns hash, finalName, finalTag, error.
	Import(ctx context.Context, filePath, name, tag string) (hash string, finalName string, finalTag string, err error)

	// ImportScenario snapshots a local scenario file into the CAS and assigns it a name:tag.
	// Returns hash, finalName, finalTag, error.
	ImportScenario(ctx context.Context, filePath, name, tag string) (hash string, finalName string, finalTag string, err error)

	// Load retrieves the content of a spec/scenario by URN (e.g. "visa:latest" or "sha256:...")
	Load(ctx context.Context, urn string) (content []byte, err error)

	// Export resolves a URN and writes its content to the given output path.
	Export(ctx context.Context, urn, outputPath string) error

	// List returns all known artifacts
	List(ctx context.Context) ([]ArtifactInfo, error)
}

type ArtifactInfo struct {
	Name string
	Tag  string
	Hash string
}
