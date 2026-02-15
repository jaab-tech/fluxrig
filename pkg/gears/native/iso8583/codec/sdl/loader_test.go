// Copyright 2025 JAAB Tech SAS, Uruguay
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

func TestLoadSpec_MissingEncoding(t *testing.T) {
	specYAML := `
meta:
  name: "fail"
fields:
  0:
    label: "MTI"
    type: "numeric"
    length: 4
`
	tmpFile := filepath.Join(t.TempDir(), "fail.yaml")
	_ = os.WriteFile(tmpFile, []byte(specYAML), 0600)
	_, _, err := LoadSpec(tmpFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing explicit 'enc'")
}
