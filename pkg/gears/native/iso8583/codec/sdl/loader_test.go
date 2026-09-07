// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moov-io/iso8583"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSpec_RealFile(t *testing.T) {
	path := filepath.Join("testdata", "generic_ascii.yaml")

	moovSpec, meta, err := LoadSpec(path)
	require.NoError(t, err)
	require.NotNil(t, moovSpec)
	require.NotNil(t, meta)

	// Check Meta
	assert.Equal(t, 12, len(meta.SpecHash))
	assert.Equal(t, "card.pan", meta.Aliases[2])
	assert.Equal(t, 2, meta.IDByAlias["card.pan"])
	assert.True(t, meta.SecureIDs[2])

	// Check Moov Spec fields
	assert.NotNil(t, moovSpec.Fields[0])
	assert.NotNil(t, moovSpec.Fields[2])
	assert.NotNil(t, moovSpec.Fields[11])

	// Check round-trip with a message
	msg := iso8583.NewMessage(moovSpec)
	err = msg.Field(0, "0200")
	require.NoError(t, err)
	err = msg.Field(2, "1234567890123456")
	require.NoError(t, err)
	err = msg.Field(11, "000001")
	require.NoError(t, err)

	packed, err := msg.Pack()
	require.NoError(t, err)
	assert.NotEmpty(t, packed)

	// Unpack
	msg2 := iso8583.NewMessage(moovSpec)
	err = msg2.Unpack(packed)
	require.NoError(t, err)

	val, err := msg2.GetString(2)
	require.NoError(t, err)
	assert.Equal(t, "1234567890123456", val)
}

func TestLoadSpec_NotFound(t *testing.T) {
	_, _, err := LoadSpec("nonexistent.yaml")
	assert.Error(t, err)
}

func TestLoadSpec_InvalidYAML(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "invalid.yaml")
	err := os.WriteFile(tmpFile, []byte("invalid: ["), 0600)
	require.NoError(t, err)

	_, _, err = LoadSpec(tmpFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// The wire layer has three origins and they compose. These are the three, plus
// the two ways a spec can fail to name one at all.
func TestWireLayerOrigins(t *testing.T) {
	const semantic = `
  fields:
    0: {name: MTI}
    11: {name: STAN, alias: stan}
`
	t.Run("a named Moov base, used as-is", func(t *testing.T) {
		spec := writeSpec(t, t.TempDir(), "s.yaml", `
spec:
  id: "base-only"
  name: "base only"
  version: "1.0.0"
  wire:
    source: "moov:spec87ascii"
`+semantic)
		moovSpec, meta, err := LoadSpec(spec)
		require.NoError(t, err)
		// The base carries the whole 1987 field set. The semantic layer names two
		// of them; the wire layer is not narrowed to what was named.
		assert.Greater(t, len(moovSpec.Fields), 60)
		assert.NotNil(t, moovSpec.Fields[2], "DE 2 comes from the base, unnamed here")
		assert.Equal(t, 11, meta.IDByAlias["stan"])
	})

	t.Run("a base with the spec's own differences over it", func(t *testing.T) {
		spec := writeSpec(t, t.TempDir(), "s.yaml", `
spec:
  id: "base-plus-delta"
  name: "base plus delta"
  version: "1.0.0"
  wire:
    source: "moov:spec87ascii"
    fields:
      11: {type: String, length: 12, enc: ASCII, prefix: ASCII.Fixed}
`+semantic)
		moovSpec, _, err := LoadSpec(spec)
		require.NoError(t, err)
		// The override wins over the base's six.
		assert.Equal(t, 12, moovSpec.Fields[11].Spec().Length)
	})

	t.Run("a wire document beside the spec", func(t *testing.T) {
		dir := t.TempDir()
		writeSpec(t, dir, "wire.yaml", `
name: "external"
fields:
  0: {description: MTI, type: String, length: 4, enc: ASCII, prefix: ASCII.Fixed}
  11: {description: STAN, type: String, length: 9, enc: ASCII, prefix: ASCII.Fixed}
`)
		spec := writeSpec(t, dir, "s.yaml", `
spec:
  id: "file-source"
  name: "file source"
  version: "1.0.0"
  wire:
    source: "wire.yaml"
`+semantic)
		moovSpec, _, err := LoadSpec(spec)
		require.NoError(t, err)
		assert.Len(t, moovSpec.Fields, 2)
		assert.Equal(t, 9, moovSpec.Fields[11].Spec().Length)
	})

	t.Run("no base at all: the spec carries its own wire layer", func(t *testing.T) {
		spec := writeSpec(t, t.TempDir(), "s.yaml", `
spec:
  id: "self-contained"
  name: "self contained"
  version: "1.0.0"
  wire:
    fields:
      0: {description: MTI, type: String, length: 4, enc: ASCII, prefix: ASCII.Fixed}
      11: {description: STAN, type: String, length: 6, enc: ASCII, prefix: ASCII.Fixed}
`+semantic)
		moovSpec, meta, err := LoadSpec(spec)
		require.NoError(t, err)
		assert.Len(t, moovSpec.Fields, 2)
		assert.Equal(t, 11, meta.IDByAlias["stan"])
	})

	t.Run("naming neither is an error, not an empty spec", func(t *testing.T) {
		spec := writeSpec(t, t.TempDir(), "s.yaml", `
spec:
  id: "nothing"
  name: "nothing"
  version: "1.0.0"
`+semantic)
		_, _, err := LoadSpec(spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no wire source")
	})

	t.Run("an unknown named base says which ones exist", func(t *testing.T) {
		spec := writeSpec(t, t.TempDir(), "s.yaml", `
spec:
  id: "unknown-base"
  name: "unknown base"
  version: "1.0.0"
  wire:
    source: "moov:spec93ascii"
`+semantic)
		_, _, err := LoadSpec(spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "moov:spec87ascii")
	})
}

// A spec is deployed to Racks by the Mixer, so the path it names is untrusted
// input. Reading through it would turn a scenario deployment into a file read on
// the edge node.
func TestFileWireSourceStaysBesideTheSpec(t *testing.T) {
	for name, source := range map[string]string{
		"climbing out":   "../secrets.yaml",
		"absolute path":  "/etc/passwd",
		"a longer climb": "sub/../../secrets.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			spec := writeSpec(t, t.TempDir(), "s.yaml", `
spec:
  id: "escape"
  name: "escape"
  version: "1.0.0"
  wire:
    source: "`+source+`"
  fields:
    0: {name: MTI}
`)
			_, _, err := LoadSpec(spec)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "no such file",
				"the path should be refused before it is opened")
		})
	}
}

func writeSpec(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}
