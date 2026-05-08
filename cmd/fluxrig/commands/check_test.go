// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckNATS(t *testing.T) {
	// Without NATS running, it should fail
	_ = os.Setenv("FLUXRIG_BUS_URL", "nats://localhost:14223") // Unlikely port
	ok := checkNATS()
	require.False(t, ok)
}

func TestCheckAPI(t *testing.T) {
	// Without API running, it should fail
	_ = os.Setenv("FLUXRIG_API_URL", "http://localhost:18091") // Unlikely port
	ok := checkAPI()
	require.False(t, ok)
}

func TestCheckStore(t *testing.T) {
	// Use a temp home to test store check
	tmpHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tmpHome)
	defer func() { _ = os.Setenv("HOME", originalHome) }()

	// 1. Initially missing (WARN -> true)
	ok := checkStore("")
	require.True(t, ok)

	// 2. Create directory
	storeDir := filepath.Join(tmpHome, ".fluxrig", "store")
	err := os.MkdirAll(storeDir, 0700)
	require.NoError(t, err)

	ok = checkStore("")
	require.True(t, ok)

	// 3. Not a directory
	_ = os.Remove(storeDir)
	err = os.WriteFile(storeDir, []byte("not-a-dir"), 0600)
	require.NoError(t, err)
	ok = checkStore("")
	require.False(t, ok)
}

func TestCheckPKI(t *testing.T) {
	tmpHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tmpHome)
	defer func() { _ = os.Setenv("HOME", originalHome) }()

	// 1. Missing (WARN -> true)
	ok := checkPKI("", "")
	require.True(t, ok)

	// 2. Partial keys
	pkiDir := filepath.Join(tmpHome, ".fluxrig", "pki")
	err := os.MkdirAll(pkiDir, 0700)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(pkiDir, "cluster.key"), []byte("test"), 0600)
	require.NoError(t, err)

	ok = checkPKI("", "")
	require.True(t, ok)

	// 3. All keys
	err = os.WriteFile(filepath.Join(pkiDir, "machine.key"), []byte("test"), 0600)
	require.NoError(t, err)
	ok = checkPKI("", "")
	require.True(t, ok)
}
