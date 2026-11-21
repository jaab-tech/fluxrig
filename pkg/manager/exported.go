// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"errors"
	"fmt"

	"golang.org/x/mod/semver"
)

// ErrTooLarge is returned when a spec is larger than the store accepts.
var ErrTooLarge = errors.New("spec larger than the store accepts")

// ParseHeader reads the name, version, title and protocol a document declares.
// Another store uses it to name an import the same way this one does.
func ParseHeader(content []byte) SpecHeader { return parseHeader(content) }

// SemverTag turns a declared version into a tag: "2.2.0" becomes "v2.2.0".
func SemverTag(v string) string { return semverTag(v) }

// ParseURN splits a name:tag or urn:flux:<kind>:<name>:<tag> reference.
func ParseURN(urn string) (name, tag string, err error) { return parseURN(urn) }

// LatestTag returns the highest valid semver tag, or "" when there is none.
func LatestTag(tags []string) string {
	var versions []string
	for _, t := range tags {
		if semver.IsValid(t) {
			versions = append(versions, t)
		}
	}
	if len(versions) == 0 {
		return ""
	}
	semver.Sort(versions)
	return versions[len(versions)-1]
}

// NextMinor returns the tag an import gets when its document declares no
// version: the minor after the highest existing tag, or v0.1.0.
func NextMinor(tags []string) string {
	latest := LatestTag(tags)
	if latest == "" {
		return "v0.1.0"
	}
	var major, minor, patch int
	if n, err := fmt.Sscanf(semver.Canonical(latest), "v%d.%d.%d", &major, &minor, &patch); err != nil || n != 3 {
		return "v0.1.0"
	}
	return fmt.Sprintf("v%d.%d.0", major, minor+1)
}

// SortArtifacts orders a listing by kind, then name, then version descending.
func SortArtifacts(a []ArtifactInfo) { sortArtifacts(a) }
