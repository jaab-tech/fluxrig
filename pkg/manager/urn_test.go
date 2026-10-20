// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseURN(t *testing.T) {
	tests := []struct {
		name     string
		urn      string
		wantName string
		wantTag  string
		wantErr  bool
	}{
		// Short form: unchanged from the previous hand-rolled split.
		{"short", "visa:latest", "visa", "latest", false},
		{"short version", "iso8583-v87-ascii:v1.2.0", "iso8583-v87-ascii", "v1.2.0", false},
		{"short with colon in name", "ns:visa:latest", "ns:visa", "latest", false},

		// Reserved scheme. On the old code these misparsed: the name picked up
		// the whole prefix.
		{"scheme spec", "urn:flux:spec:visa:latest", "visa", "latest", false},
		{"scheme scenario", "urn:flux:scenario:flow:v1.0.0", "flow", "v1.0.0", false},
		{"scheme name with colon", "urn:flux:spec:ns:visa:latest", "ns:visa", "latest", false},

		// Malformed: an error, never a wrong lookup.
		{"empty", "", "", "", true},
		{"no colon", "visa", "", "", true},
		{"leading colon", ":x", "", "", true},
		{"trailing colon", "x:", "", "", true},
		{"scheme missing tag", "urn:flux:spec:visa", "", "", true},
		{"scheme missing name and tag", "urn:flux:spec:", "", "", true},
		{"scheme bad kind", "urn:flux:gear:visa:latest", "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, tag, err := parseURN(tc.urn)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantName, name)
			assert.Equal(t, tc.wantTag, tag)
		})
	}
}

// A fully qualified urn must resolve the same artifact as its short form: this
// is the point of parsing it, and the reason the hand split was a bug.
func TestManager_LoadByFullyQualifiedURN(t *testing.T) {
	mgr, err := NewManager(t.TempDir())
	require.NoError(t, err)

	specPath := writeSpec(t, t.TempDir(), "visa")
	_, _, _, err = mgr.Import(context.Background(), specPath, "visa", "v1.0.0")
	require.NoError(t, err)

	short, err := mgr.Load(context.Background(), "visa:v1.0.0")
	require.NoError(t, err)

	full, err := mgr.Load(context.Background(), "urn:flux:spec:visa:v1.0.0")
	require.NoError(t, err, "a fully qualified urn must resolve")

	assert.Equal(t, short, full, "both forms name the same artifact")
}
