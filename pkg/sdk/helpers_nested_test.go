// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaab-tech/fluxrig/pkg/fluxmsg"
)

// TestGetValueNestedSurvivesTheBus pins the defect that made every nested key
// field silently stop working the moment a message crossed a rack boundary.
//
// A message built in process holds nested objects as map[string]any. CBOR
// decodes them as map[any]any, so the same field has one shape before the bus
// and another after it. Resolving only the first shape meant a correlation key
// worked in a unit test, worked in a single-gear pipeline, and returned "field
// missing" in a real deployment, with nothing logged to say why.
func TestGetValueNestedSurvivesTheBus(t *testing.T) {
	build := func() *fluxmsg.FluxMsg {
		return &fluxmsg.FluxMsg{
			Data: map[string]any{
				"iso8583": map[string]any{
					"field": map[string]any{"41": "TERM0001"},
				},
			},
			Metadata: map[string]string{},
		}
	}

	local := build()
	v, ok := GetValue(local, "data.iso8583.field.41")
	require.True(t, ok, "a nested path must resolve before the bus")
	assert.Equal(t, "TERM0001", v)

	blob, err := cbor.Marshal(build())
	require.NoError(t, err)
	var arrived fluxmsg.FluxMsg
	require.NoError(t, cbor.Unmarshal(blob, &arrived))

	v, ok = GetValue(&arrived, "data.iso8583.field.41")
	require.True(t, ok, "the same path must resolve after the bus; if this fails, "+
		"nested correlation keys are broken in every multi-gear deployment")
	assert.Equal(t, "TERM0001", v)
}
