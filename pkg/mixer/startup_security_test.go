// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package mixer

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A non-TLS entry anywhere in the external NATS URL list warns; an all-TLS
// list, or none at all, stays quiet.
func TestExternalNATSUsesPlaintext(t *testing.T) {
	assert.True(t, externalNATSUsesPlaintext("nats://nats.example:4222"))
	assert.True(t, externalNATSUsesPlaintext("tls://a:4222,nats://b:4222"),
		"one plaintext entry is enough")
	assert.True(t, externalNATSUsesPlaintext("TLS://A:4222, NATS://B:4222"),
		"the scheme match is case-insensitive")
	assert.False(t, externalNATSUsesPlaintext("tls://nats.example:4222"))
	assert.False(t, externalNATSUsesPlaintext("tls://a:4222, tls://b:4222"))
	assert.False(t, externalNATSUsesPlaintext(""), "no URL configures nothing to warn about")
}

// Only an explicit http:// OTLP endpoint warns: https stays quiet, and a
// missing endpoint fails later with its own validation error, not here.
func TestOTLPUsesPlaintext(t *testing.T) {
	assert.True(t, otlpUsesPlaintext("http://collector:4318"))
	assert.True(t, otlpUsesPlaintext("HTTP://collector:4318"))
	assert.False(t, otlpUsesPlaintext("https://collector.example:4318"))
	assert.False(t, otlpUsesPlaintext(""))
}

// A key file anyone on the machine can read warns with its path; an
// owner-only file, an unset path, or a missing file stays quiet (the missing
// one fails elsewhere, where its own error names it).
func TestWarnWorldReadable(t *testing.T) {
	dir := t.TempDir()
	open := filepath.Join(dir, "open.key")
	shut := filepath.Join(dir, "shut.key")
	require.NoError(t, os.WriteFile(open, []byte("k"), 0o644))
	require.NoError(t, os.WriteFile(shut, []byte("k"), 0o600))

	var logs bytes.Buffer
	warnWorldReadable(slog.New(slog.NewTextHandler(&logs, nil)),
		open, shut, "", filepath.Join(dir, "missing.key"))

	out := logs.String()
	assert.Contains(t, out, open, "the open file warns with its path")
	assert.NotContains(t, out, shut, "the owner-only file stays quiet")
	assert.NotContains(t, out, "missing.key", "a missing file fails elsewhere, not here")
}
