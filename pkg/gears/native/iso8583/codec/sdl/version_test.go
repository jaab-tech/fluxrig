// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// minimalSpec is the smallest document the loader accepts, with the header left
// to the caller so a test can state exactly what it is changing.
func minimalSpec(header string) []byte {
	return []byte(`spec:
` + header + `
  protocol: iso8583
  wire:
    format: moov
    source: "moov:spec87ascii"
  fields:
    2:
      alias: pan
`)
}

// A spec states a contract, and a contract nobody can refer to is not one. The
// schema asked for a version and nothing enforced it, so a spec with no version
// at all loaded clean -- and the trace it produced said only which bytes ran,
// never which contract they were.
func TestASpecMustDeclareTheContractItStates(t *testing.T) {
	cases := []struct {
		name, header, wants string
	}{
		{"no version", "  id: acme\n  name: Acme", "spec.version is required"},
		{"empty version", "  id: acme\n  version: \"\"", "spec.version is required"},
		{"blank version", "  id: acme\n  version: \"   \"", "spec.version is required"},
		{"not a version", "  id: acme\n  version: \"latest\"", "not a semantic version"},
		{"two parts only", "  id: acme\n  version: \"2.2\"", "not a semantic version"},
		{"no identity", "  version: \"1.0.0\"", "spec.id is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := LoadSpecContent(minimalSpec(c.header), "")
			require.Error(t, err, "the spec loaded without a contract to name")
			require.Contains(t, err.Error(), c.wants)
		})
	}
}

// The refusal names the spec it is about. A Rack loads several, and an error
// saying only "spec.version is required" leaves the operator to guess which.
func TestTheRefusalNamesTheSpec(t *testing.T) {
	_, _, err := LoadSpecContent(minimalSpec("  id: acme-auth\n  name: Acme"), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"acme-auth"`)
}

// A team ships a release candidate before it ships the release.
func TestAPreReleaseVersionIsAVersion(t *testing.T) {
	for _, v := range []string{"1.0.0", "v1.0.0", "2.2.0-rc1", "0.1.0", "10.20.30"} {
		_, meta, err := LoadSpecContent(minimalSpec("  id: acme\n  version: \""+v+"\""), "")
		require.NoError(t, err, "version %q was refused", v)
		require.Equal(t, v, meta.SpecVersion)
	}
}

// The identity travels with the version because the version alone does not name
// a contract: a Rack running two codecs sees two specs both calling themselves
// 1.0.0, and the reference suite has exactly that -- payment-switch-auth and
// payment-switch, same version, different protocols.
func TestTheLoadedSpecCarriesItsIdentity(t *testing.T) {
	_, meta, err := LoadSpec(filepath.Join("testdata", "generic_ascii.yaml"))
	require.NoError(t, err)
	require.Equal(t, "generic-ascii", meta.SpecID)
	require.Equal(t, "1.0.0", meta.SpecVersion)
	// The hash is still there and is still not the version: it proves which
	// file, and it moves when a comment does.
	require.NotEmpty(t, meta.SpecHash)
	require.NotEqual(t, meta.SpecVersion, meta.SpecHash)
}

// Every spec that ships in this repository has to satisfy the rule the loader
// now enforces, or the rule is one nobody can adopt.
func TestEverySpecInTheRepositoryDeclaresAVersion(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..", "..")
	var checked int
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			// A directory that cannot be read is not this test's business, but
			// the vendored module cache is full of unrelated "spec" documents.
			if info != nil && info.IsDir() && (info.Name() == ".gomod_cache" || info.Name() == ".git") {
				return filepath.SkipDir
			}
			return nil //nolint:nilerr // an unreadable path is not a spec
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		data, readErr := os.ReadFile(filepath.Clean(path))
		// A spec opens with a licence header, so the key is a line rather than
		// the first byte. Looking only at the first byte found five of thirteen,
		// and a walk that misses most of what it walks proves nothing.
		if readErr != nil || !isSpecDocument(data) {
			return nil
		}
		checked++
		if _, _, loadErr := LoadSpec(path); loadErr != nil {
			// Only the contract rule is this test's business; a spec may fail to
			// load for reasons of its own that other tests own.
			if strings.Contains(loadErr.Error(), "spec.version") || strings.Contains(loadErr.Error(), "spec.id") {
				t.Errorf("%s: %v", path, loadErr)
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, checked, 12, "found only %d specs; the walk is not reaching them", checked)
}

// isSpecDocument reports whether a YAML file is one the SDL loader would read.
func isSpecDocument(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimRight(line, " \t\r") == "spec:" {
			return true
		}
	}
	return false
}

// The schema is published for people who never read the loader, and the loader
// is what actually runs. When they disagree, an editor shows a spec as valid
// that a Rack then refuses -- or worse, the other way round. This is the test
// that keeps the two honest; the samples are the whole disagreement they had.
func TestTheSchemaAndTheLoaderAgreeOnWhatAVersionIs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "..",
		"examples", "specs", "spec.core.schema.json"))
	require.NoError(t, err)

	var schema struct {
		Properties struct {
			Spec struct {
				Properties struct {
					Version struct {
						Pattern string `json:"pattern"`
					} `json:"version"`
				} `json:"properties"`
			} `json:"spec"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	pattern := schema.Properties.Spec.Properties.Version.Pattern
	require.NotEmpty(t, pattern, "the schema no longer constrains the version")

	fromSchema, err := regexp.Compile(pattern)
	require.NoError(t, err, "the published pattern does not compile under RE2, so every Go validator rejects it")

	for _, v := range []string{
		"1.0.0", "v1.0.0", "2.2.0-rc1", "0.1.0", "10.20.30", // accepted
		"", "latest", "2.2", "1.0.0.0", "01.0.0", "v", "-1.0.0", // refused
	} {
		bySchema := fromSchema.MatchString(v)
		byLoader := specVersionRe.MatchString(v)
		require.Equal(t, bySchema, byLoader,
			"version %q: the schema says %v and the loader says %v", v, bySchema, byLoader)
	}
}
