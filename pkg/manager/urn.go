// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"fmt"
	"strings"
)

// urnFluxPrefix is the reserved scheme for a fully qualified artifact
// reference: urn:flux:spec:name:tag or urn:flux:scenario:name:tag. The short
// name:tag form is the one scenarios write today; the scheme is the documented
// future form, and it must parse now so it does not arrive as a misread.
const urnFluxPrefix = "urn:flux:"

// parseURN turns an artifact reference into its name and tag.
//
// Two forms are accepted:
//
//	name:tag                          the short form, split at the last colon
//	urn:flux:{spec|scenario}:name:tag the reserved scheme, split into four
//	                                  tokens; the kind token is accepted and
//	                                  ignored, because specs and scenarios
//	                                  share one index and Load already tries both
//
// Anything else yields an empty name or tag and an error. The old code found
// the last colon by hand, so a urn:flux: reference read name="urn:flux:spec:visa"
// and tag="latest" and silently looked up the wrong name. This names the
// malformed cases instead.
func parseURN(urn string) (name, tag string, err error) {
	if urn == "" {
		return "", "", fmt.Errorf("invalid URN format: %s (expected name:tag or hash)", urn)
	}

	if strings.HasPrefix(urn, urnFluxPrefix) {
		rest := strings.TrimPrefix(urn, urnFluxPrefix)
		parts := strings.SplitN(rest, ":", 3)
		// kind:name:tag, where name may itself contain colons: SplitN(3) keeps
		// everything after the kind in parts[2] (name:tag), and the final split
		// takes the tag from the last colon of that body.
		if len(parts) < 3 {
			return "", "", fmt.Errorf("invalid URN format: %s (expected urn:flux:<kind>:<name>:<tag>)", urn)
		}
		kind := parts[0]
		if kind != "spec" && kind != "scenario" {
			return "", "", fmt.Errorf("invalid URN kind %q in %s (expected spec or scenario)", kind, urn)
		}
		body := parts[1] + ":" + parts[2]
		idx := strings.LastIndex(body, ":")
		if idx < 0 {
			return "", "", fmt.Errorf("invalid URN format: %s (expected urn:flux:<kind>:<name>:<tag>)", urn)
		}
		name, tag = body[:idx], body[idx+1:]
		if name == "" || tag == "" {
			return "", "", fmt.Errorf("invalid URN format: %s (expected urn:flux:<kind>:<name>:<tag>)", urn)
		}
		return name, tag, nil
	}

	// Short form: name:tag, split at the last colon, exactly as the previous
	// hand-rolled loop did, so every existing reference resolves identically.
	idx := strings.LastIndex(urn, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("invalid URN format: %s (expected name:tag or hash)", urn)
	}
	name, tag = urn[:idx], urn[idx+1:]
	if name == "" || tag == "" {
		return "", "", fmt.Errorf("invalid URN format: %s (expected name:tag or hash)", urn)
	}
	return name, tag, nil
}
