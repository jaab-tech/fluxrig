// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/moov-io/iso8583"
)

type FieldMeta struct {
	// SpecHash proves which file; SpecID and SpecVersion state which contract.
	// Both are wanted and neither substitutes for the other: the hash is
	// unforgeable but changes when a comment does, and it says nothing to a
	// human reading a trace.
	SpecHash    string
	SpecID      string
	SpecVersion string
	Protocol    string
	Aliases     map[int]string
	SubAliases  map[int]map[string]string // Field ID -> SubKey -> Alias
	IDByAlias   map[string]int
	SecureIDs   map[int]bool
}

// LoadSpec reads a spec file and returns the Moov MessageSpec it describes,
// along with the semantic metadata the pipeline addresses fields by.
//
// One vocabulary reaches here. The wire half is Moov's own, consumed rather than
// restated; the semantic half is fluxrig's. Where the wire layer comes from — a
// named Moov base, a wire document beside the spec, or the spec's own
// `wire.fields` — is resolved by WireDocument, and the three compose.
func LoadSpec(path string) (*iso8583.MessageSpec, *FieldMeta, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read spec file: %w", err)
	}
	return LoadSpecContent(data, filepath.Dir(path))
}

// LoadSpecContent compiles a spec that is already in hand — resolved from the
// content-addressed store rather than read from a path.
//
// baseDir is what a file wire source resolves against, and a spec that arrived
// as content has no directory: pass "" and a relative wire source is refused
// with that reason, rather than resolving against whatever the process happens
// to have as its working directory.
func LoadSpecContent(data []byte, baseDir string) (*iso8583.MessageSpec, *FieldMeta, error) {
	hash := sha256.Sum256(data)
	return load(data, baseDir, hex.EncodeToString(hash[:])[:12])
}

// specVersionRe is semver without the build metadata and with an optional
// leading v, matching what the CAS orders tags by and what the schema declares.
// A spec is not a Go module, so the pre-release tail is allowed but not parsed:
// `2.2.0-rc1` is a version a team will genuinely deploy.
var specVersionRe = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)

// validateSpecVersion refuses a spec that cannot be referred to.
//
// The identity is checked alongside it because a version alone does not name a
// contract: a Rack running two codecs sees two specs both calling themselves
// 1.0.0, and a trace saying only "1.0.0" cannot tell them apart.
func validateSpecVersion(id, version string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("spec.id is required: a version is meaningless without the identity it versions")
	}
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("spec %q: spec.version is required", id)
	}
	if !specVersionRe.MatchString(version) {
		return fmt.Errorf("spec %q: spec.version %q is not a semantic version (expected MAJOR.MINOR.PATCH)", id, version)
	}
	return nil
}
